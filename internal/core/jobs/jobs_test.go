package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/policy"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

type fixture struct {
	t     *testing.T
	store *storage.Store
	bus   *events.Log
	pol   *policy.Store
	q     *Queue
	mu    sync.Mutex
	now   time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	store, err := storage.Open(ctx, storage.Options{Path: filepath.Join(t.TempDir(), "jobs.db")})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	bus, err := events.New(store, store, events.Options{})
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	pol, err := policy.New(store, store, bus, nil)
	if err != nil {
		t.Fatalf("policy: %v", err)
	}

	f := &fixture{t: t, store: store, bus: bus, pol: pol, now: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)}
	q, err := New(store, store, bus, pol, Options{
		PollInterval: 20 * time.Millisecond,
		LeaseTTL:     time.Second,
		Heartbeat:    40 * time.Millisecond,
		CancelGrace:  200 * time.Millisecond,
		Now:          f.clock,
	})
	if err != nil {
		t.Fatalf("jobs: %v", err)
	}
	f.q = q
	return f
}

func (f *fixture) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fixture) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

func (f *fixture) start() {
	ctx, cancel := context.WithCancel(context.Background())
	f.q.Start(ctx)
	f.t.Cleanup(func() {
		f.q.Stop()
		cancel()
	})
}

func (f *fixture) enable(pluginID string) {
	f.t.Helper()
	ctx := context.Background()
	if err := f.pol.Register(ctx, pluginID, false); err != nil {
		f.t.Fatal(err)
	}
	if err := f.pol.Enable(ctx, pluginID, "test", "setup"); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) waitState(id int64, want State, d time.Duration) *Job {
	f.t.Helper()
	deadline := time.Now().Add(d)
	var last *Job
	for time.Now().Before(deadline) {
		j, err := f.q.Get(context.Background(), id)
		if err == nil {
			last = j
			if j.State == want {
				return j
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	if last == nil {
		f.t.Fatalf("job %d never appeared, want %s", id, want)
	}
	f.t.Fatalf("job %d state = %s, want %s (err=%q)", id, last.State, want, last.LastError)
	return last
}

func TestEnqueueRunsToSuccess(t *testing.T) {
	f := newFixture(t)
	f.enable("hello")
	var ran atomic.Int32
	if err := f.q.Register("hello", Def{
		Name: "work",
		Handler: func(jc Context) error {
			ran.Add(1)
			var args map[string]string
			if err := jc.Args(&args); err != nil {
				return err
			}
			if args["k"] != "v" {
				t.Errorf("args = %v", args)
			}
			if err := jc.Progress(1, "done"); err != nil {
				return err
			}
			return jc.Logf("ok")
		},
	}); err != nil {
		t.Fatal(err)
	}
	f.start()

	id, err := f.q.Enqueue(context.Background(), "hello", "work", map[string]string{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}
	j := f.waitState(id, StateSucceeded, 2*time.Second)
	if ran.Load() != 1 {
		t.Fatalf("ran %d times", ran.Load())
	}
	if j.Progress != 1 {
		t.Errorf("progress = %v", j.Progress)
	}
	if len(j.Logs) != 1 || j.Logs[0].Line != "ok" {
		t.Errorf("logs = %+v", j.Logs)
	}
}

func TestDisabledPluginCannotEnqueue(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.pol.Register(ctx, "hello", false); err != nil {
		t.Fatal(err)
	}
	if err := f.q.Register("hello", Def{Name: "work", Handler: func(Context) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	_, err := f.q.Enqueue(ctx, "hello", "work", nil)
	if !errors.Is(err, policy.ErrPluginDisabled) {
		t.Fatalf("got %v, want ErrPluginDisabled", err)
	}
}

func TestUnknownDefIsRejected(t *testing.T) {
	f := newFixture(t)
	f.enable("hello")
	_, err := f.q.Enqueue(context.Background(), "hello", "missing", nil)
	if !errors.Is(err, ErrUnknownDef) {
		t.Fatalf("got %v", err)
	}
}

func TestIdempotencyReturnsTheExistingJob(t *testing.T) {
	f := newFixture(t)
	f.enable("hello")
	block := make(chan struct{})
	if err := f.q.Register("hello", Def{
		Name: "work",
		Handler: func(jc Context) error {
			<-block
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	f.start()

	id1, err := f.q.Enqueue(context.Background(), "hello", "work", nil, WithIdempotencyKey("once"))
	if err != nil {
		t.Fatal(err)
	}
	id2, err := f.q.Enqueue(context.Background(), "hello", "work", nil, WithIdempotencyKey("once"))
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("got %d and %d", id1, id2)
	}
	close(block)
	f.waitState(id1, StateSucceeded, 2*time.Second)
	id3, err := f.q.Enqueue(context.Background(), "hello", "work", nil, WithIdempotencyKey("once"))
	if err != nil {
		t.Fatal(err)
	}
	if id3 == id1 {
		t.Fatal("key should be reusable after the job is terminal")
	}
}

func TestRetryThenDead(t *testing.T) {
	f := newFixture(t)
	f.enable("hello")
	var n atomic.Int32
	if err := f.q.Register("hello", Def{
		Name:        "work",
		MaxAttempts: 2,
		Backoff:     BackoffPolicy{Initial: time.Millisecond, Maximum: time.Millisecond},
		Handler: func(Context) error {
			n.Add(1)
			return errors.New("boom")
		},
	}); err != nil {
		t.Fatal(err)
	}
	f.start()
	id, err := f.q.Enqueue(context.Background(), "hello", "work", nil)
	if err != nil {
		t.Fatal(err)
	}
	j := f.waitState(id, StateDead, 3*time.Second)
	if n.Load() != 2 {
		t.Fatalf("attempts ran = %d, want 2", n.Load())
	}
	if j.LastErrorClass != ClassError {
		t.Errorf("class = %q", j.LastErrorClass)
	}
}

func TestPermanentDoesNotRetry(t *testing.T) {
	f := newFixture(t)
	f.enable("hello")
	var n atomic.Int32
	if err := f.q.Register("hello", Def{
		Name:        "work",
		MaxAttempts: 5,
		Handler: func(Context) error {
			n.Add(1)
			return Permanent(errors.New("bad args"))
		},
	}); err != nil {
		t.Fatal(err)
	}
	f.start()
	id, err := f.q.Enqueue(context.Background(), "hello", "work", nil)
	if err != nil {
		t.Fatal(err)
	}
	j := f.waitState(id, StateFailed, 2*time.Second)
	if n.Load() != 1 {
		t.Fatalf("ran %d, want 1", n.Load())
	}
	if j.LastErrorClass != ClassPermanent {
		t.Errorf("class = %q", j.LastErrorClass)
	}
}

func TestCancelPending(t *testing.T) {
	f := newFixture(t)
	f.enable("hello")
	if err := f.q.Register("hello", Def{
		Name: "work",
		Handler: func(Context) error {
			t.Fatal("handler should not run")
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	id, err := f.q.Enqueue(context.Background(), "hello", "work", nil, WithRunAt(f.now.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.q.Cancel(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	j, err := f.q.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if j.State != StateCancelled || j.CancelReason != ReasonUser {
		t.Fatalf("%s %s", j.State, j.CancelReason)
	}
}

func TestCancelRunning(t *testing.T) {
	f := newFixture(t)
	f.enable("hello")
	started := make(chan struct{})
	if err := f.q.Register("hello", Def{
		Name:    "work",
		Timeout: 5 * time.Second,
		Handler: func(jc Context) error {
			close(started)
			<-jc.Done()
			return jc.Err()
		},
	}); err != nil {
		t.Fatal(err)
	}
	f.start()
	id, err := f.q.Enqueue(context.Background(), "hello", "work", nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}
	if err := f.q.Cancel(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	j := f.waitState(id, StateCancelled, 2*time.Second)
	if j.CancelReason != ReasonUser {
		t.Errorf("reason = %q", j.CancelReason)
	}
}

func TestDisableCancelsRunningAndRejectsClaim(t *testing.T) {
	f := newFixture(t)
	f.enable("hello")
	started := make(chan struct{})
	if err := f.q.Register("hello", Def{
		Name:    "work",
		Timeout: 5 * time.Second,
		Handler: func(jc Context) error {
			close(started)
			<-jc.Done()
			return jc.Err()
		},
	}); err != nil {
		t.Fatal(err)
	}
	f.start()
	id, err := f.q.Enqueue(context.Background(), "hello", "work", nil)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := f.pol.Disable(context.Background(), "hello", "op", "kill"); err != nil {
		t.Fatal(err)
	}
	j := f.waitState(id, StateCancelled, 2*time.Second)
	if j.CancelReason != ReasonPluginDisabled {
		t.Errorf("reason = %q", j.CancelReason)
	}

	id2, err := f.q.Enqueue(context.Background(), "hello", "work", nil, WithRunAt(f.now.Add(time.Hour)))
	if err == nil {
		_ = id2
		t.Fatal("enqueue after disable should fail")
	}
	if !errors.Is(err, policy.ErrPluginDisabled) {
		t.Fatalf("got %v", err)
	}
}

func TestTimeoutIsRetryableThenDead(t *testing.T) {
	f := newFixture(t)
	f.enable("hello")
	if err := f.q.Register("hello", Def{
		Name:        "work",
		Timeout:     30 * time.Millisecond,
		MaxAttempts: 1,
		Handler: func(jc Context) error {
			<-jc.Done()
			return jc.Err()
		},
	}); err != nil {
		t.Fatal(err)
	}
	f.start()
	id, err := f.q.Enqueue(context.Background(), "hello", "work", nil)
	if err != nil {
		t.Fatal(err)
	}
	j := f.waitState(id, StateDead, 3*time.Second)
	if j.LastErrorClass != ClassTimeout {
		t.Errorf("class = %q err=%q", j.LastErrorClass, j.LastError)
	}
}

func TestPanicIsRetryable(t *testing.T) {
	f := newFixture(t)
	f.enable("hello")
	var n atomic.Int32
	if err := f.q.Register("hello", Def{
		Name:        "work",
		MaxAttempts: 2,
		Backoff:     BackoffPolicy{Initial: time.Millisecond, Maximum: time.Millisecond},
		Handler: func(Context) error {
			n.Add(1)
			panic("nope")
		},
	}); err != nil {
		t.Fatal(err)
	}
	f.start()
	id, err := f.q.Enqueue(context.Background(), "hello", "work", nil)
	if err != nil {
		t.Fatal(err)
	}
	j := f.waitState(id, StateDead, 3*time.Second)
	if n.Load() != 2 {
		t.Fatalf("ran %d", n.Load())
	}
	if j.LastErrorClass != ClassPanic {
		t.Errorf("class = %q", j.LastErrorClass)
	}
}

func TestProgressRejectedAfterCancel(t *testing.T) {
	f := newFixture(t)
	f.enable("hello")
	started := make(chan struct{})
	got := make(chan error, 1)
	if err := f.q.Register("hello", Def{
		Name:    "work",
		Timeout: 5 * time.Second,
		Handler: func(jc Context) error {
			close(started)
			<-jc.Done()
			got <- jc.Progress(0.5, "late")
			return jc.Err()
		},
	}); err != nil {
		t.Fatal(err)
	}
	f.start()
	id, err := f.q.Enqueue(context.Background(), "hello", "work", nil)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := f.q.Cancel(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-got:
		if err != nil && !errors.Is(err, ErrLostLease) && !errors.Is(err, context.Canceled) {
			t.Fatalf("progress: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return")
	}
	f.waitState(id, StateCancelled, 2*time.Second)
}

func TestCronFiresWithoutCatchUp(t *testing.T) {
	f := newFixture(t)
	f.enable("hello")
	var n atomic.Int32
	if err := f.q.Register("hello", Def{
		Name:     "tick",
		Schedule: "* * * * *",
		TimeZone: "UTC",
		Handler: func(Context) error {
			n.Add(1)
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	f.start()
	deadline := time.Now().Add(2 * time.Second)
	for n.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n.Load() != 1 {
		t.Fatalf("first tick ran %d times", n.Load())
	}
	// Jump two minutes; the skipped minute must not be enqueued.
	f.advance(2 * time.Minute)
	f.q.signal()
	deadline = time.Now().Add(2 * time.Second)
	for n.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if n.Load() != 2 {
		t.Fatalf("after jump ran %d times, want 2 (no catch-up)", n.Load())
	}
}

func TestCatchUpSchedulesSkipsCurrentSlot(t *testing.T) {
	f := newFixture(t)
	f.enable("hello")
	var n atomic.Int32
	def := Def{
		Name:     "tick",
		Schedule: "* * * * *",
		TimeZone: "UTC",
		Handler: func(Context) error {
			n.Add(1)
			return nil
		},
	}
	if err := f.q.Register("hello", def); err != nil {
		t.Fatal(err)
	}
	f.start()
	deadline := time.Now().Add(2 * time.Second)
	for n.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n.Load() != 1 {
		t.Fatalf("first tick ran %d times", n.Load())
	}

	f.q.UnregisterAll("hello")
	f.advance(time.Minute)
	if err := f.q.CatchUpSchedules(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if err := f.q.Register("hello", def); err != nil {
		t.Fatal(err)
	}
	f.q.signal()
	time.Sleep(200 * time.Millisecond)
	if n.Load() != 1 {
		t.Fatalf("re-register caught up skipped slot, ran %d times", n.Load())
	}
}

func TestCronDisabledTickIsDropped(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.pol.Register(ctx, "hello", false); err != nil {
		t.Fatal(err)
	}
	if err := f.q.Register("hello", Def{
		Name:     "tick",
		Schedule: "* * * * *",
		TimeZone: "UTC",
		Handler: func(Context) error {
			t.Fatal("disabled cron must not run")
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	f.start()
	time.Sleep(200 * time.Millisecond)
	all, err := f.q.List(ctx, Filter{PluginID: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Fatalf("enqueued %d jobs while disabled", len(all))
	}
}

func TestScopedRejectsAnotherPluginsJob(t *testing.T) {
	f := newFixture(t)
	f.enable("hello")
	f.enable("other")
	if err := f.q.Register("hello", Def{Name: "work", Handler: func(Context) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	id, err := f.q.Enqueue(context.Background(), "hello", "work", nil, WithRunAt(f.now.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	s := Scoped(f.q, "other")
	if _, err := s.Get(context.Background(), id); !errors.Is(err, ErrPluginMismatch) {
		t.Fatalf("get: %v", err)
	}
	if err := s.Cancel(context.Background(), id); !errors.Is(err, ErrPluginMismatch) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestCronParseAndMatch(t *testing.T) {
	mustParse := func(expr string) *cronSched {
		t.Helper()
		c, err := parseCron(expr)
		if err != nil {
			t.Fatalf("parse %q: %v", expr, err)
		}
		return c
	}
	at := func(hour, min int) time.Time {
		return time.Date(2026, 8, 30, hour, min, 0, 0, time.UTC) // Sunday
	}

	c := mustParse("0 3 * * *")
	if !c.matches(at(3, 0)) {
		t.Fatal("should match 03:00")
	}
	if c.matches(at(3, 1)) {
		t.Fatal("should not match 03:01")
	}

	steps := mustParse("*/15 0 * * *")
	for _, min := range []int{0, 15, 30, 45} {
		if !steps.matches(at(0, min)) {
			t.Fatalf("*/15 should match minute %d", min)
		}
	}
	if steps.matches(at(0, 7)) {
		t.Fatal("*/15 should not match minute 7")
	}

	ranged := mustParse("1-5 2 * * *")
	if !ranged.matches(at(2, 3)) || ranged.matches(at(2, 6)) {
		t.Fatal("1-5 should include 3 and exclude 6")
	}

	listed := mustParse("1,30,59 4 * * *")
	if !listed.matches(at(4, 30)) || listed.matches(at(4, 31)) {
		t.Fatal("list should include 30 and exclude 31")
	}

	steppedRange := mustParse("1-10/2 5 * * *")
	if !steppedRange.matches(at(5, 1)) || !steppedRange.matches(at(5, 9)) || steppedRange.matches(at(5, 2)) {
		t.Fatal("1-10/2 should hit odd minutes only")
	}

	sunday7 := mustParse("0 0 * * 7")
	if !sunday7.matches(at(0, 0)) {
		t.Fatal("dow 7 must be Sunday")
	}
	monday := mustParse("0 0 * * 1")
	if monday.matches(at(0, 0)) {
		t.Fatal("Sunday is not Monday")
	}

	// Both DOM and DOW restricted: classic cron ORs them.
	firstOrMonday := mustParse("0 0 1 * 1")
	first := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)  // Tuesday
	mon := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)   // Monday
	other := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)  // Wednesday
	if !firstOrMonday.matches(first) || !firstOrMonday.matches(mon) || firstOrMonday.matches(other) {
		t.Fatal("restricted DOM and DOW must OR")
	}

	for _, bad := range []string{
		"0 3 * *",
		"60 * * * *",
		"* 24 * * *",
		"5-1 * * * *",
		"*/0 * * * *",
		"*/foo * * * *",
		"1-10/0 * * * *",
		"a * * * *",
		"* * 0 * *",
		"* * * 13 *",
	} {
		if _, err := parseCron(bad); err == nil {
			t.Errorf("parseCron(%q) should fail", bad)
		}
	}
}

func TestListFilters(t *testing.T) {
	f := newFixture(t)
	f.enable("hello")
	if err := f.q.Register("hello", Def{Name: "work", Handler: func(Context) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	id, err := f.q.Enqueue(context.Background(), "hello", "work", map[string]int{"n": 1}, WithRunAt(f.now.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	all, err := f.q.List(context.Background(), Filter{PluginID: "hello", State: StatePending})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].ID != id {
		t.Fatalf("%+v", all)
	}
	raw := map[string]any{}
	if err := json.Unmarshal([]byte(all[0].Args), &raw); err != nil || raw["n"] != float64(1) {
		t.Fatalf("args %s", all[0].Args)
	}
}

// Every enqueue leaves a durable row, so without a sweep the table only ever grows. A
// finished job is eligible once it is old enough; work still owed to someone never is,
// whatever its age.
func TestRetentionDeletesOnlyOldFinishedJobs(t *testing.T) {
	f := newFixture(t)
	f.enable("hello")
	if err := f.q.Register("hello", Def{
		Name:    "work",
		Handler: func(jc Context) error { return jc.Logf("ok") },
	}); err != nil {
		t.Fatal(err)
	}
	f.start()

	done, err := f.q.Enqueue(context.Background(), "hello", "work", nil)
	if err != nil {
		t.Fatal(err)
	}
	f.waitState(done, StateSucceeded, 2*time.Second)

	// A job that has not run yet: still the queue's to deliver, however long it waits.
	pending, err := f.q.Enqueue(context.Background(), "hello", "work", nil,
		WithRunAt(f.clock().Add(365*24*time.Hour)))
	if err != nil {
		t.Fatal(err)
	}

	// Not yet old enough.
	rep, err := f.q.RunRetention(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Jobs != 0 {
		t.Fatalf("swept %d jobs before the age limit", rep.Jobs)
	}
	if _, err := f.q.Get(context.Background(), done); err != nil {
		t.Fatalf("finished job disappeared early: %v", err)
	}

	f.advance(15 * 24 * time.Hour)
	rep, err = f.q.RunRetention(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Jobs != 1 {
		t.Fatalf("swept %d jobs, want 1", rep.Jobs)
	}
	if rep.Logs != 1 {
		t.Fatalf("swept %d log lines, want 1", rep.Logs)
	}
	if _, err := f.q.Get(context.Background(), done); err == nil {
		t.Fatal("finished job survived the sweep")
	}
	if _, err := f.q.Get(context.Background(), pending); err != nil {
		t.Fatalf("pending job was swept: %v", err)
	}
}
