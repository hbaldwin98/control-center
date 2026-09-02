package bidrl_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hbaldwin98/control-center/host/hosttest"
	"github.com/hbaldwin98/control-center/plugins/bidrl"
)

// The scheduled half of the plugin is the part nobody watches run, so its guards are
// the ones worth pinning: every reason a tick declines to touch BidRL, and the note it
// leaves behind saying which reason it was.

type autoConfig struct {
	Enabled             bool     `json:"enabled"`
	AffiliateIDs        []string `json:"affiliateIds"`
	MaxAuctionsPerSweep int      `json:"maxAuctionsPerSweep"`
	MaxNewLotsPerSweep  int      `json:"maxNewLotsPerSweep"`
}

// newAutomationHarness brings up the plugin against the fake BidRL site with the
// automation block configured as the test wants it.
func newAutomationHarness(t *testing.T, ctx context.Context, auto autoConfig) *hosttest.Harness {
	t.Helper()
	h := hosttest.New(t, bidrl.New())
	site := bidrl.FakeSite()
	h.Browser.Handle("www.bidrl.com", site)
	h.Browser.Handle("bidrl.com", site)
	installModels(h.AI)
	installSearch(h.Search)
	h.Run(ctx)
	h.SetConfig(ctx, map[string]any{
		"preferredAffiliateIds": []string{},
		"searchScope":           "prefer",
		"automation":            auto,
	})
	return h
}

// automationView is what GET /automation reports; the tick notes are read back from it
// rather than from the table, because that is the surface an operator actually sees.
type automationView struct {
	Automation struct {
		Enabled        bool   `json:"enabled"`
		Locations      int    `json:"locations"`
		SweepSchedule  string `json:"sweepSchedule"`
		MatchSchedule  string `json:"matchSchedule"`
		TimeZone       string `json:"timeZone"`
		LastSweepNote  string `json:"lastSweepNote"`
		LastSweepAt    string `json:"lastSweepAt"`
		LastMatchNote  string `json:"lastMatchNote"`
		LastMatchAt    string `json:"lastMatchAt"`
		ThrottledUntil string `json:"throttledUntil"`
		Throttled      bool   `json:"throttled"`
		NewFindings    int    `json:"newFindings"`
	} `json:"automation"`
}

func readAutomation(t *testing.T, h *hosttest.Harness) automationView {
	t.Helper()
	var got automationView
	h.DecodeJSON(h.GET("/automation"), http.StatusOK, &got)
	return got
}

func TestSweepDoesNothingWhenAutomationIsOff(t *testing.T) {
	ctx := context.Background()
	h := newAutomationHarness(t, ctx, autoConfig{Enabled: false, AffiliateIDs: []string{"19"}})

	if err := h.RunJobNow(ctx, "sweep", nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := h.Browser.Visited(); len(got) != 0 {
		t.Fatalf("a disabled sweep must not touch BidRL, visited %v", got)
	}
	if note := readAutomation(t, h).Automation.LastSweepNote; note != "automation is off" {
		t.Fatalf("note = %q", note)
	}
}

// An empty location list is the second off switch: turning automation on is not by
// itself permission to crawl the whole site.
func TestSweepDoesNothingWithNoLocations(t *testing.T) {
	ctx := context.Background()
	h := newAutomationHarness(t, ctx, autoConfig{Enabled: true, AffiliateIDs: nil})

	if err := h.RunJobNow(ctx, "sweep", nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := h.Browser.Visited(); len(got) != 0 {
		t.Fatalf("an unscoped sweep must not touch BidRL, visited %v", got)
	}
	note := readAutomation(t, h).Automation.LastSweepNote
	if !strings.Contains(note, "no locations") {
		t.Fatalf("note = %q", note)
	}
}

// The latch is the whole point of the throttle: a later tick must decline before it
// opens a browser session at all, not merely be polite once it has one.
func TestSweepDeclinesWhileTheThrottleIsLatched(t *testing.T) {
	ctx := context.Background()
	h := newAutomationHarness(t, ctx, autoConfig{Enabled: true, AffiliateIDs: []string{"19"}})

	until := h.Clock.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	if _, err := h.DB().Exec(`UPDATE bidrl_automation SET throttled_until = ? WHERE id = 1`, until); err != nil {
		t.Fatal(err)
	}
	if got := readAutomation(t, h); !got.Automation.Throttled {
		t.Fatal("automation should report itself throttled")
	}

	if err := h.RunJobNow(ctx, "sweep", nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := h.Browser.Visited(); len(got) != 0 {
		t.Fatalf("a latched sweep must not touch BidRL, visited %v", got)
	}
	if note := readAutomation(t, h).Automation.LastSweepNote; !strings.Contains(note, "throttled") {
		t.Fatalf("note = %q", note)
	}
}

// An expired latch stops holding on its own; only an unexpired one blocks.
func TestSweepRunsOnceTheLatchHasExpired(t *testing.T) {
	ctx := context.Background()
	h := newAutomationHarness(t, ctx, autoConfig{Enabled: true, AffiliateIDs: []string{"19"}})

	past := h.Clock.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	if _, err := h.DB().Exec(`UPDATE bidrl_automation SET throttled_until = ? WHERE id = 1`, past); err != nil {
		t.Fatal(err)
	}
	if got := readAutomation(t, h); got.Automation.Throttled {
		t.Fatal("an expired latch should not report as throttled")
	}
	if err := h.RunJobNow(ctx, "sweep", nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(h.Browser.Visited()) == 0 {
		t.Fatal("an expired latch should let the sweep run")
	}
}

// The happy path: the sweep discovers the configured location's open auction, collects
// it, and says what it collected.
func TestSweepCollectsTheConfiguredLocation(t *testing.T) {
	ctx := context.Background()
	h := newAutomationHarness(t, ctx, autoConfig{Enabled: true, AffiliateIDs: []string{"19"}})

	if err := h.RunJobNow(ctx, "sweep", nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if err := h.Drain(ctx); err != nil {
		t.Fatalf("follow-on job: %v", err)
	}

	var lots int
	if err := h.DB().QueryRow(`SELECT COUNT(*) FROM bidrl_lots WHERE auction_id = '42'`).Scan(&lots); err != nil {
		t.Fatal(err)
	}
	if lots != 3 {
		t.Fatalf("sweep collected %d lots, want the fake auction's 3", lots)
	}
	note := readAutomation(t, h).Automation.LastSweepNote
	if !strings.Contains(note, "collected") {
		t.Fatalf("note = %q", note)
	}
	if !publishedEvent(h, "sweep.completed") {
		t.Fatalf("sweep did not publish sweep.completed, events %v", eventTypes(h))
	}
}

// A second tick over the same auctions has nothing new to do, and says so rather than
// re-collecting what it already has.
func TestSecondSweepFindsNothingNew(t *testing.T) {
	ctx := context.Background()
	h := newAutomationHarness(t, ctx, autoConfig{Enabled: true, AffiliateIDs: []string{"19"}})

	if err := h.RunJobNow(ctx, "sweep", nil); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if err := h.Drain(ctx); err != nil {
		t.Fatalf("follow-on job: %v", err)
	}
	if err := h.RunJobNow(ctx, "sweep", nil); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	note := readAutomation(t, h).Automation.LastSweepNote
	if !strings.Contains(note, "nothing new") {
		t.Fatalf("note = %q", note)
	}
}

// maxAuctionsPerSweep is the per-tick budget; it has to hold even when more auctions
// are available than the budget allows.
func TestSweepHonoursTheAuctionBudget(t *testing.T) {
	ctx := context.Background()
	h := newAutomationHarness(t, ctx, autoConfig{
		Enabled: true, AffiliateIDs: []string{"19"}, MaxAuctionsPerSweep: 1,
	})
	if err := h.RunJobNow(ctx, "sweep", nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if err := h.Drain(ctx); err != nil {
		t.Fatalf("follow-on job: %v", err)
	}
	var auctions int
	if err := h.DB().QueryRow(`SELECT COUNT(*) FROM bidrl_auctions`).Scan(&auctions); err != nil {
		t.Fatal(err)
	}
	if auctions > 1 {
		t.Fatalf("budget of 1 collected %d auctions", auctions)
	}
}

func TestMatchDoesNothingWhenAutomationIsOff(t *testing.T) {
	ctx := context.Background()
	h := newAutomationHarness(t, ctx, autoConfig{Enabled: false})

	if err := h.RunJobNow(ctx, "match", nil); err != nil {
		t.Fatalf("match: %v", err)
	}
	if note := readAutomation(t, h).Automation.LastMatchNote; note != "automation is off" {
		t.Fatalf("note = %q", note)
	}
	if len(h.AI.Calls()) != 0 {
		t.Fatal("a disabled match must not call a model")
	}
}

func TestMatchWithNoEnabledWatchlists(t *testing.T) {
	ctx := context.Background()
	h := newAutomationHarness(t, ctx, autoConfig{Enabled: true, AffiliateIDs: []string{"19"}})

	if err := h.RunJobNow(ctx, "match", nil); err != nil {
		t.Fatalf("match: %v", err)
	}
	if note := readAutomation(t, h).Automation.LastMatchNote; note != "no enabled watchlists" {
		t.Fatalf("note = %q", note)
	}
}

// A disabled watchlist is not an enabled one: the tick must skip it rather than count
// it and find nothing.
func TestMatchSkipsDisabledWatchlists(t *testing.T) {
	ctx := context.Background()
	h := newAutomationHarness(t, ctx, autoConfig{Enabled: true, AffiliateIDs: []string{"19"}})

	id := createWatchlist(t, h, map[string]any{"name": "Coffee", "query": "keurig coffee maker"})
	rec := h.Do(patch(t, "/watchlists/"+id, map[string]any{"enabled": false}))
	if rec.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body.Bytes())
	}

	if err := h.RunJobNow(ctx, "match", nil); err != nil {
		t.Fatalf("match: %v", err)
	}
	if note := readAutomation(t, h).Automation.LastMatchNote; note != "no enabled watchlists" {
		t.Fatalf("note = %q", note)
	}
}

// The match tick over a collected catalog: it runs the enabled watchlist's funnel and
// reports how many findings came out of it.
func TestMatchRunsEnabledWatchlists(t *testing.T) {
	ctx := context.Background()
	h := newAutomationHarness(t, ctx, autoConfig{Enabled: true, AffiliateIDs: []string{"19"}})

	if err := h.RunJobNow(ctx, "sweep", nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if err := h.Drain(ctx); err != nil {
		t.Fatalf("follow-on job: %v", err)
	}
	createWatchlist(t, h, map[string]any{"name": "Coffee", "query": "keurig coffee maker"})

	if err := h.RunJobNow(ctx, "match", nil); err != nil {
		t.Fatalf("match: %v", err)
	}
	if err := h.Drain(ctx); err != nil {
		t.Fatalf("follow-on job: %v", err)
	}

	note := readAutomation(t, h).Automation.LastMatchNote
	if !strings.Contains(note, "from 1 watchlists") {
		t.Fatalf("note = %q", note)
	}
	if !publishedEvent(h, "match.completed") {
		t.Fatalf("match did not publish match.completed, events %v", eventTypes(h))
	}
	// The watchlist row carries its own outcome, which is what the screen reads.
	var status string
	if err := h.DB().QueryRow(`SELECT status FROM bidrl_watchlists`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status == "running" || status == "failed" {
		t.Fatalf("watchlist left in status %q", status)
	}
}

// ------------------------------------------------------------------ the endpoints

func TestGetAutomationReportsTheSchedule(t *testing.T) {
	ctx := context.Background()
	h := newAutomationHarness(t, ctx, autoConfig{Enabled: true, AffiliateIDs: []string{"19", "20"}})

	got := readAutomation(t, h).Automation
	if !got.Enabled || got.Locations != 2 {
		t.Fatalf("config not reflected: %+v", got)
	}
	if got.SweepSchedule == "" || got.MatchSchedule == "" || got.TimeZone != "UTC" {
		t.Fatalf("schedule not reported: %+v", got)
	}
}

// Only a person clears the latch, so the endpoint that does it has to actually clear
// it — and a tick has to be willing to run again afterwards.
func TestResumeClearsTheLatch(t *testing.T) {
	ctx := context.Background()
	h := newAutomationHarness(t, ctx, autoConfig{Enabled: true, AffiliateIDs: []string{"19"}})

	until := h.Clock.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	if _, err := h.DB().Exec(`UPDATE bidrl_automation SET throttled_until = ? WHERE id = 1`, until); err != nil {
		t.Fatal(err)
	}
	if rec := h.POST("/automation/resume", nil); rec.Code != http.StatusOK {
		t.Fatalf("resume: %d %s", rec.Code, rec.Body.Bytes())
	}
	if got := readAutomation(t, h); got.Automation.Throttled || got.Automation.ThrottledUntil != "" {
		t.Fatalf("latch not cleared: %+v", got.Automation)
	}
	if err := h.RunJobNow(ctx, "sweep", nil); err != nil {
		t.Fatalf("sweep after resume: %v", err)
	}
	if len(h.Browser.Visited()) == 0 {
		t.Fatal("sweep should run once the latch is cleared")
	}
}

func TestAutomationEndpointsRefuseWhileDisabled(t *testing.T) {
	ctx := context.Background()
	h := newAutomationHarness(t, ctx, autoConfig{Enabled: true, AffiliateIDs: []string{"19"}})
	h.Disable()

	for _, rec := range []*httptest.ResponseRecorder{
		h.GET("/automation"),
		h.POST("/automation/resume", nil),
		h.GET("/findings"),
	} {
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("got %d, want 503: %s", rec.Code, rec.Body.Bytes())
		}
	}
}

// ------------------------------------------------------------------- helpers

func createWatchlist(t *testing.T, h *hosttest.Harness, body map[string]any) string {
	t.Helper()
	rec := h.POST("/watchlists", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create watchlist: %d %s", rec.Code, rec.Body.Bytes())
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.ID == "" {
		t.Fatalf("create body %s", rec.Body.Bytes())
	}
	return out.ID
}

func patch(t *testing.T, path string, body any) *http.Request {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func eventTypes(h *hosttest.Harness) []string {
	var out []string
	for _, e := range h.Events() {
		out = append(out, e.Type)
	}
	return out
}

// The host namespaces a plugin's events with its id, so match on the plugin-side
// name rather than the wire one.
func publishedEvent(h *hosttest.Harness, typ string) bool {
	for _, e := range h.Events() {
		if e.Type == typ || strings.HasSuffix(e.Type, "."+typ) {
			return true
		}
	}
	return false
}
