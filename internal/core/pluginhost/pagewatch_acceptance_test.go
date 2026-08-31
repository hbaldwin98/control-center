package pluginhost

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/ai"
	"github.com/hbaldwin98/control-center/internal/core/jobs"
	"github.com/hbaldwin98/control-center/internal/core/policy"
)

func TestPagewatchCapabilityPipeline(t *testing.T) {
	f := newHelloFix(t, false)
	if err := f.pol.SetBudget(f.ctx, "pagewatch", policy.Budget{
		Daily: 50_000_000, OnExceed: policy.ExceedReject,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.reg.Enable(f.ctx, "pagewatch", "test", "acceptance"); err != nil {
		t.Fatal(err)
	}

	run := func() int64 {
		t.Helper()
		rec := f.serve(http.MethodPost, "/api/plugins/pagewatch/checks", nil)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("enqueue check: %d %s", rec.Code, rec.Body.Bytes())
		}
		var posted struct {
			JobID int64 `json:"jobId"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &posted); err != nil || posted.JobID == 0 {
			t.Fatalf("enqueue body: %s", rec.Body.Bytes())
		}
		f.waitJob(posted.JobID, jobs.StateSucceeded, 3*time.Second)
		return posted.JobID
	}
	waitChecks := func(want int) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			var got int
			if err := f.store.QueryRow(f.ctx, `SELECT count(*) FROM pagewatch_checks`).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got == want {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("pagewatch check history did not reach %d rows", want)
	}

	run()
	waitChecks(1)
	assertLatestPagewatchCheck(t, f, "baseline", true, true)

	run()
	waitChecks(2)
	assertLatestPagewatchCheck(t, f, "unchanged", true, false)
	calls, err := f.ai.Calls(f.ctx, ai.CallQuery{PluginID: "pagewatch"})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls.Calls) != 1 {
		t.Fatalf("unchanged second check made %d total AI calls, want 1", len(calls.Calls))
	}

	rec := f.serve(http.MethodGet, "/api/plugins/pagewatch/checks", nil)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte("Example Domain")) {
		t.Fatalf("list checks: %d %s", rec.Code, rec.Body.Bytes())
	}
	rc, _, err := f.blobs.Scoped("pagewatch").Get(f.ctx, "latest.txt")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || !strings.Contains(string(snapshot), "Example Domain") {
		t.Fatalf("snapshot = %q, err = %v", snapshot, err)
	}

	if err := f.reg.UpdateConfig(f.ctx, "pagewatch", json.RawMessage(
		`{"url":"https://example.com/","expectedText":"Definitely absent"}`,
	), "test"); err != nil {
		t.Fatal(err)
	}
	run()
	waitChecks(3)
	assertLatestPagewatchCheck(t, f, "attention", false, true)
	var alerts int
	if err := f.store.QueryRow(f.ctx,
		`SELECT count(*) FROM core_events WHERE type = 'pagewatch.alert'`).Scan(&alerts); err != nil {
		t.Fatal(err)
	}
	if alerts != 1 {
		t.Fatalf("alert events = %d, want 1", alerts)
	}
}

func assertLatestPagewatchCheck(t *testing.T, f *helloFix, status string, expectedFound, aiRan bool) {
	t.Helper()
	var gotStatus string
	var gotExpected, gotAI int
	if err := f.store.QueryRow(f.ctx, `SELECT status, expected_found, ai_ran
		FROM pagewatch_checks ORDER BY id DESC LIMIT 1`).Scan(&gotStatus, &gotExpected, &gotAI); err != nil {
		t.Fatal(err)
	}
	if gotStatus != status || (gotExpected != 0) != expectedFound || (gotAI != 0) != aiRan {
		t.Fatalf("latest check = status %q, expected %t, AI %t; want %q, %t, %t",
			gotStatus, gotExpected != 0, gotAI != 0, status, expectedFound, aiRan)
	}
}
