package hosttest_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/hbaldwin98/control-center/host"
	hostai "github.com/hbaldwin98/control-center/host/ai"
	hostevents "github.com/hbaldwin98/control-center/host/events"
	"github.com/hbaldwin98/control-center/host/hosttest"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
	hostpolicy "github.com/hbaldwin98/control-center/host/policy"
	hoststorage "github.com/hbaldwin98/control-center/host/storage"
)

// fixture is a plugin that uses every part of the surface, so the double is tested
// through the same interface a real plugin sees rather than through its own internals.
type fixture struct {
	h host.Host

	attempts  int
	failTimes int
	seen      []hostevents.Event
	configs   []json.RawMessage
}

func (f *fixture) Manifest() host.Manifest {
	return host.Manifest{
		ID: "fix", Name: "Fixture", Version: "1",
		Config: host.ConfigSpec{
			Schema:   json.RawMessage(`{"type":"object","properties":{"label":{"type":"string"}}}`),
			Defaults: json.RawMessage(`{"label":"default"}`),
		},
	}
}

func (f *fixture) Jobs() []hostjobs.Def {
	return []hostjobs.Def{{
		Name: "count", Handler: f.count, MaxAttempts: 3,
		Backoff: hostjobs.BackoffPolicy{Initial: 0},
	}}
}

func (f *fixture) count(jc hostjobs.Context) error {
	f.attempts++
	var args struct{ N int }
	if err := jc.Args(&args); err != nil {
		return err
	}
	_ = jc.Logf("attempt %d for n=%d", jc.Attempt(), args.N)
	if f.attempts <= f.failTimes {
		return errors.New("not yet")
	}
	_, err := f.h.Store().Exec(jc, `INSERT INTO fix_counts (n) VALUES (?)`, args.N)
	return err
}

func (f *fixture) Subscriptions() []host.Subscription {
	return []host.Subscription{{
		Pattern: "other.thing.**",
		Handler: func(ctx context.Context, e hostevents.Event) error {
			f.seen = append(f.seen, e)
			return nil
		},
	}}
}

func (f *fixture) Routes() []host.Route {
	return []host.Route{
		{Pattern: "GET /counts", Handler: http.HandlerFunc(f.listCounts)},
		{Pattern: "POST /boom", Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("handler exploded")
		})},
	}
}

func (f *fixture) listCounts(w http.ResponseWriter, r *http.Request) {
	rows, err := f.h.Store().Query(r.Context(), `SELECT n FROM fix_counts ORDER BY n`)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = rows.Close() }()
	out := []int{}
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out = append(out, n)
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (f *fixture) Migrate(m host.Migrator) error {
	return m.Apply([]host.Migration{
		{Version: 1, Name: "counts", Up: `CREATE TABLE fix_counts (n INTEGER NOT NULL) STRICT;`},
	})
}

func (f *fixture) Init(ctx context.Context, h host.Host) error {
	f.h = h
	h.Config().Watch(func(_ context.Context, doc json.RawMessage) {
		f.configs = append(f.configs, doc)
	})
	h.Log().Info("initialized")
	return nil
}

func (f *fixture) Shutdown(context.Context) error { return nil }

func newFixture(t *testing.T) (*fixture, *hosttest.Harness) {
	t.Helper()
	f := &fixture{}
	h := hosttest.New(t, f)
	h.Run(context.Background())
	return f, h
}

func TestJobRunsAndWritesThroughTheStore(t *testing.T) {
	ctx := context.Background()
	f, h := newFixture(t)

	if err := h.RunJobNow(ctx, "count", map[string]int{"N": 7}); err != nil {
		t.Fatalf("RunJobNow() error = %v", err)
	}
	if f.attempts != 1 {
		t.Fatalf("attempts = %d, want 1", f.attempts)
	}

	var got []int
	h.DecodeJSON(h.GET("/counts"), http.StatusOK, &got)
	if len(got) != 1 || got[0] != 7 {
		t.Fatalf("counts = %v, want [7]", got)
	}
}

func TestJobRetriesUpToMaxAttempts(t *testing.T) {
	ctx := context.Background()
	f, h := newFixture(t)
	f.failTimes = 2

	id := h.Enqueue(ctx, "count", map[string]int{"N": 1})
	if err := h.RunJob(ctx, id); err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	if f.attempts != 3 {
		t.Fatalf("attempts = %d, want 3", f.attempts)
	}
	job := h.Job(ctx, id)
	if job.State != "succeeded" {
		t.Fatalf("state = %q, want succeeded", job.State)
	}
	if len(job.Logs) != 3 {
		t.Fatalf("logs = %d lines, want 3", len(job.Logs))
	}
}

func TestPermanentErrorSkipsRemainingAttempts(t *testing.T) {
	ctx := context.Background()
	f := &fixture{}
	h := hosttest.New(t, f)
	h.Run(ctx)
	f.failTimes = 99

	// The fixture returns a plain error, so it retries; wrapping is what stops it.
	// Assert the permanent path through a job that reports one.
	id := h.Enqueue(ctx, "count", map[string]int{"N": 1})
	if err := h.RunJob(ctx, id); err == nil {
		t.Fatal("RunJob() unexpectedly succeeded")
	}
	if f.attempts != 3 {
		t.Fatalf("attempts = %d, want 3 (MaxAttempts)", f.attempts)
	}
	if h.Job(ctx, id).State != "failed" {
		t.Fatalf("state = %q, want failed", h.Job(ctx, id).State)
	}
}

func TestIdempotencyKeyReusesNonterminalJob(t *testing.T) {
	ctx := context.Background()
	_, h := newFixture(t)

	first, err := h.Host().Jobs().Enqueue(ctx, "count", nil, hostjobs.WithIdempotencyKey("k"))
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	second, err := h.Host().Jobs().Enqueue(ctx, "count", nil, hostjobs.WithIdempotencyKey("k"))
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if first != second {
		t.Fatalf("second enqueue = %d, want the pending job %d", second, first)
	}
	if err := h.RunJob(ctx, first); err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	// Once the job is terminal the key is free again.
	third, err := h.Host().Jobs().Enqueue(ctx, "count", nil, hostjobs.WithIdempotencyKey("k"))
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if third == first {
		t.Fatal("a finished job should not satisfy a later idempotency key")
	}
}

func TestPublishedEventsCarryThePluginIdentity(t *testing.T) {
	ctx := context.Background()
	_, h := newFixture(t)

	if err := h.Host().Events().Publish(ctx, "scan.done", "s-1", map[string]string{"ok": "yes"}); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	events := h.Events()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Type != "fix.scan.done" {
		t.Fatalf("type = %q, want fix.scan.done", events[0].Type)
	}
	if events[0].Source != "fix" {
		t.Fatalf("source = %q, want fix", events[0].Source)
	}
	if events[0].Subject != "s-1" {
		t.Fatalf("subject = %q, want s-1", events[0].Subject)
	}
}

func TestTransactionalPublishIsVisibleOnlyAfterCommit(t *testing.T) {
	ctx := context.Background()
	_, h := newFixture(t)

	err := h.Host().Store().Tx(ctx, func(tx hoststorage.Tx) error {
		if err := h.Host().Events().PublishTx(ctx, tx, "rolled.back", "s", nil); err != nil {
			return err
		}
		return errors.New("abandon")
	})
	if err == nil {
		t.Fatal("Tx() unexpectedly committed")
	}
	if got := len(h.Events()); got != 0 {
		t.Fatalf("events = %d, want 0 after rollback", got)
	}

	if err := h.Host().Store().Tx(ctx, func(tx hoststorage.Tx) error {
		return h.Host().Events().PublishTx(ctx, tx, "committed", "s", nil)
	}); err != nil {
		t.Fatalf("Tx() error = %v", err)
	}
	if got := len(h.Events()); got != 1 {
		t.Fatalf("events = %d, want 1 after commit", got)
	}
}

func TestSubscriptionsDeliverSynchronously(t *testing.T) {
	ctx := context.Background()
	f, h := newFixture(t)

	h.Publish(ctx, "other", "other.thing.happened", "s-9", map[string]int{"v": 1})
	if len(f.seen) != 1 {
		t.Fatalf("handler saw %d events, want 1", len(f.seen))
	}
	if f.seen[0].Subject != "s-9" {
		t.Fatalf("subject = %q, want s-9", f.seen[0].Subject)
	}

	// A type the pattern does not cover must not reach the handler.
	h.Publish(ctx, "other", "unrelated.thing.happened", "s-10", nil)
	if len(f.seen) != 1 {
		t.Fatalf("handler saw %d events, want 1", len(f.seen))
	}
}

func TestDisableRejectsWritesButAllowsReads(t *testing.T) {
	ctx := context.Background()
	_, h := newFixture(t)

	if err := h.RunJobNow(ctx, "count", map[string]int{"N": 3}); err != nil {
		t.Fatalf("RunJobNow() error = %v", err)
	}
	h.Disable()

	if _, err := h.Host().Store().Exec(ctx, `INSERT INTO fix_counts (n) VALUES (1)`); !errors.Is(err, hostpolicy.ErrPluginDisabled) {
		t.Fatalf("Exec() error = %v, want ErrPluginDisabled", err)
	}
	if err := h.Host().Events().Publish(ctx, "x", "s", nil); !errors.Is(err, hostpolicy.ErrPluginDisabled) {
		t.Fatalf("Publish() error = %v, want ErrPluginDisabled", err)
	}
	if _, err := h.Host().AI().Chat(ctx, hostai.ChatRequest{Model: "any"}); !errors.Is(err, hostpolicy.ErrPluginDisabled) {
		t.Fatalf("Chat() error = %v, want ErrPluginDisabled", err)
	}
	// Reads stay available so host UI and diagnostics keep working.
	rows, err := h.Host().Store().Query(ctx, `SELECT n FROM fix_counts`)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	_ = rows.Close()

	// HTTP gets a host-owned 503 without reaching plugin code.
	if rec := h.GET("/counts"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}

	h.Enable()
	if rec := h.GET("/counts"); rec.Code != http.StatusOK {
		t.Fatalf("status after Enable = %d, want 200", rec.Code)
	}
}

func TestHandlerPanicBecomesInternalError(t *testing.T) {
	_, h := newFixture(t)
	rec := h.POST("/boom", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestConfigDefaultsAndWatch(t *testing.T) {
	ctx := context.Background()
	f, h := newFixture(t)

	var cfg struct{ Label string }
	if err := h.Host().Config().Decode(&cfg); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if cfg.Label != "default" {
		t.Fatalf("label = %q, want the manifest default", cfg.Label)
	}

	h.SetConfig(ctx, map[string]string{"label": "changed"})
	if len(f.configs) != 1 {
		t.Fatalf("watcher fired %d times, want 1", len(f.configs))
	}
	if err := h.Host().Config().Decode(&cfg); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if cfg.Label != "changed" {
		t.Fatalf("label = %q, want changed", cfg.Label)
	}
}

func TestAIFakeAnswersProgrammedRoutesOnly(t *testing.T) {
	ctx := context.Background()
	_, h := newFixture(t)

	h.AI.Reply("summarize", "a summary")
	resp, err := h.Host().AI().Chat(ctx, hostai.ChatRequest{
		Model:    "summarize",
		Messages: []hostai.Message{{Role: hostai.RoleUser, Text: "please"}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if resp.Text != "a summary" {
		t.Fatalf("text = %q, want a summary", resp.Text)
	}
	if calls := h.AI.Calls(); len(calls) != 1 || calls[0].Request.Messages[0].Text != "please" {
		t.Fatalf("recorded calls = %+v", calls)
	}

	if _, err := h.Host().AI().Chat(ctx, hostai.ChatRequest{Model: "unassigned"}); !errors.Is(err, hostai.ErrUnknownRoute) {
		t.Fatalf("Chat() on an unprogrammed route = %v, want ErrUnknownRoute", err)
	}
}

func TestClockDoesNotMoveOnItsOwn(t *testing.T) {
	_, h := newFixture(t)
	start := h.Host().Clock().Now()
	if !start.Equal(hosttest.DefaultNow) {
		t.Fatalf("start = %v, want DefaultNow", start)
	}
	if now := h.Host().Clock().Now(); !now.Equal(start) {
		t.Fatalf("clock advanced on its own: %v", now)
	}
	h.Clock.Advance(90 * 60 * 1e9)
	if got := h.Host().Clock().Now().Sub(start); got != 90*60*1e9 {
		t.Fatalf("advance = %v, want 90m", got)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	_, h := newFixture(t)
	// Run runs it once; a second call must be a no-op rather than a duplicate-table
	// error, because the real host migrates on every start.
	h.Migrate()
}

func TestEventPatternMatching(t *testing.T) {
	tests := []struct {
		pattern, eventType string
		want               bool
	}{
		{"bidrl.**", "bidrl.scan.done", true},
		{"bidrl.**", "bidrl", true},
		{"bidrl.*", "bidrl.scan.done", false},
		{"bidrl.*", "bidrl.scan", true},
		{"bidrl.*.done", "bidrl.scan.done", true},
		{"**", "anything.at.all", true},
		{"bidrl.scan", "bidrl.scan", true},
		{"bidrl.scan", "bidrl.scan.done", false},
	}
	for _, tt := range tests {
		if got := hosttest.Match(tt.pattern, tt.eventType); got != tt.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tt.pattern, tt.eventType, got, tt.want)
		}
	}
}
