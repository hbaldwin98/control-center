package pagewatch_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	hostpolicy "github.com/hbaldwin98/control-center/host/policy"

	"github.com/hbaldwin98/control-center/host/hosttest"
	"github.com/hbaldwin98/control-center/plugins/pagewatch"
)

const examplePage = `<!doctype html><html><body><main>
	<h1>Example Domain</h1>
	<p>This domain is for use in illustrative examples in documents.</p>
</main></body></html>`

// fixture starts the plugin against the host double with one page served.
func fixture(t *testing.T) (context.Context, *hosttest.Harness) {
	t.Helper()
	ctx := context.Background()
	h := hosttest.New(t, pagewatch.New())
	h.Browser.Page("https://example.com/", examplePage)
	h.AI.Reply("cheap-chat", "The page describes an example domain.")
	h.Run(ctx)
	return ctx, h
}

// runCheck posts a check the way the UI does and runs the job it enqueued.
func runCheck(t *testing.T, ctx context.Context, h *hosttest.Harness) {
	t.Helper()
	var posted struct {
		JobID int64 `json:"jobId"`
	}
	h.DecodeJSON(h.POST("/checks", nil), http.StatusAccepted, &posted)
	if posted.JobID == 0 {
		t.Fatal("POST /checks returned no job id")
	}
	if err := h.RunJob(ctx, posted.JobID); err != nil {
		t.Fatalf("check job failed: %v", err)
	}
	if state := h.Job(ctx, posted.JobID).State; state != "succeeded" {
		t.Fatalf("job state = %q, want succeeded", state)
	}
}

func TestCapabilityPipeline(t *testing.T) {
	ctx, h := fixture(t)

	// First check has nothing to compare against, so it is a baseline and the model
	// runs.
	runCheck(t, ctx, h)
	assertLatestCheck(t, h, "baseline", true, true)

	// The page has not changed and the clock has not moved past the AI interval, so
	// the second check reuses the stored summary instead of paying for another call.
	runCheck(t, ctx, h)
	assertLatestCheck(t, h, "unchanged", true, false)
	if calls := h.AI.Calls(); len(calls) != 1 {
		t.Fatalf("AI calls = %d, want 1: an unchanged page must not pay for a second call", len(calls))
	}

	// The history endpoint reports what was seen.
	rec := h.GET("/checks")
	h.DecodeJSON(rec, http.StatusOK, nil)
	if !strings.Contains(rec.Body.String(), "Example Domain") {
		t.Fatalf("GET /checks does not mention the page: %s", rec.Body.String())
	}

	// The visible text is kept as a blob so a later check can diff against it.
	rc, _, err := h.Host().Blobs().Get(ctx, "latest.txt")
	if err != nil {
		t.Fatalf("snapshot blob: %v", err)
	}
	snapshot, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if !strings.Contains(string(snapshot), "Example Domain") {
		t.Fatalf("snapshot = %q", snapshot)
	}

	// Expected text the page does not contain is the alerting case.
	h.SetConfig(ctx, map[string]string{
		"url":          "https://example.com/",
		"expectedText": "Definitely absent",
	})
	runCheck(t, ctx, h)
	assertLatestCheck(t, h, "attention", false, true)

	var alerts int
	for _, e := range h.Events() {
		if e.Type == "pagewatch.alert" {
			alerts++
		}
	}
	if alerts != 1 {
		t.Fatalf("alert events = %d, want 1; published %+v", alerts, h.Events())
	}
}

func TestUnreachableTargetFailsTheJob(t *testing.T) {
	ctx, h := fixture(t)
	h.SetConfig(ctx, map[string]string{"url": "https://nothing-here.example/", "expectedText": ""})

	err := h.RunJobNow(ctx, "check", nil)
	if err == nil {
		t.Fatal("a target that cannot be reached unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "browser") {
		t.Fatalf("error = %v, want the failure reported as a browser failure", err)
	}
}

func TestDisabledPluginRejectsChecks(t *testing.T) {
	ctx, h := fixture(t)
	h.Disable()

	if rec := h.POST("/checks", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /checks while disabled = %d, want 503", rec.Code)
	}
	// Enqueueing is refused too, so nothing accumulates to run when the plugin comes
	// back.
	if _, err := h.Host().Jobs().Enqueue(ctx, "check", nil); !errors.Is(err, hostpolicy.ErrPluginDisabled) {
		t.Fatalf("Enqueue() while disabled = %v, want ErrPluginDisabled", err)
	}
}

func assertLatestCheck(t *testing.T, h *hosttest.Harness, status string, expectedFound, aiRan bool) {
	t.Helper()
	var gotStatus string
	var gotExpected, gotAI int
	err := h.DB().QueryRow(`SELECT status, expected_found, ai_ran
		FROM pagewatch_checks ORDER BY id DESC LIMIT 1`).Scan(&gotStatus, &gotExpected, &gotAI)
	if err != nil {
		t.Fatalf("read latest check: %v", err)
	}
	if gotStatus != status || (gotExpected != 0) != expectedFound || (gotAI != 0) != aiRan {
		t.Fatalf("latest check = status %q, expected %t, AI %t; want %q, %t, %t",
			gotStatus, gotExpected != 0, gotAI != 0, status, expectedFound, aiRan)
	}
}
