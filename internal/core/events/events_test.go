package events

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/storage"
)

func newLog(t *testing.T, tweak func(*Options)) (*storage.Store, *Log) {
	t.Helper()
	store, err := storage.Open(context.Background(),
		storage.Options{Path: filepath.Join(t.TempDir(), "events.db")})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	opts := Options{PollInterval: 10 * time.Millisecond}
	if tweak != nil {
		tweak(&opts)
	}
	l, err := New(store, store, opts)
	if err != nil {
		t.Fatalf("events.New: %v", err)
	}
	return store, l
}

func startLog(t *testing.T, l *Log) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	l.Start(ctx)
	t.Cleanup(func() { cancel(); l.Stop() })
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestPublishAndQuery(t *testing.T) {
	ctx := context.Background()
	_, l := newLog(t, nil)

	id1, err := l.Publish(ctx, Input{Type: "core.job.enqueued", Source: SourceJobs,
		Subject: "job-1", Payload: map[string]any{"plugin": "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := l.Publish(ctx, Input{Type: "bidrl.deal_found", Source: "bidrl", Subject: "lot-9"})
	if err != nil {
		t.Fatal(err)
	}
	if id2 <= id1 {
		t.Fatalf("IDs are not monotonic: %d then %d", id1, id2)
	}

	all, err := l.Query(ctx, Query{Pattern: "**"})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("read %d events, want 2", len(all))
	}
	if all[0].ID != id1 || all[1].ID != id2 {
		t.Fatal("events are not in increasing ID order")
	}
	if string(all[0].Payload) != `{"plugin":"hello"}` {
		t.Fatalf("payload = %s", all[0].Payload)
	}
	if all[0].CreatedAt.IsZero() {
		t.Fatal("missing createdAt")
	}

	only, err := l.Query(ctx, Query{Pattern: "bidrl.**"})
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 1 || only[0].ID != id2 {
		t.Fatalf("pattern query returned %+v", only)
	}

	after, err := l.Query(ctx, Query{Pattern: "**", AfterID: id1})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].ID != id2 {
		t.Fatalf("AfterID query returned %+v", after)
	}
}

func TestPublishTxRollbackRemovesTheEvent(t *testing.T) {
	ctx := context.Background()
	store, l := newLog(t, nil)

	sentinel := errors.New("rolled back")
	err := store.Tx(ctx, func(tx storage.Tx) error {
		if _, err := l.PublishTx(ctx, tx, Input{Type: "core.job.failed", Source: SourceJobs}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("got %v", err)
	}

	tail, err := l.Tail(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tail != 0 {
		t.Fatalf("a rolled-back event survived: tail = %d", tail)
	}
}

func TestPublishValidatesInput(t *testing.T) {
	ctx := context.Background()
	_, l := newLog(t, nil)

	if _, err := l.Publish(ctx, Input{Type: "notatype", Source: SourceJobs}); !errors.Is(err, ErrInvalidType) {
		t.Errorf("got %v, want ErrInvalidType", err)
	}
	if _, err := l.Publish(ctx, Input{Type: "core.job.failed", Source: "bidrl"}); !errors.Is(err, ErrInvalidSource) {
		t.Errorf("got %v, want ErrInvalidSource", err)
	}
	big := strings.Repeat("x", MaxPayloadBytes+1)
	if _, err := l.Publish(ctx, Input{Type: "core.job.failed", Source: SourceJobs, Payload: big}); !errors.Is(err, ErrPayloadTooBig) {
		t.Errorf("got %v, want ErrPayloadTooBig", err)
	}
}

func TestLiveSubscriptionDelivers(t *testing.T) {
	ctx := context.Background()
	_, l := newLog(t, nil)
	startLog(t, l)

	var mu sync.Mutex
	var seen []string
	sub, err := l.SubscribeLive("bidrl.**", func(_ context.Context, e Event) error {
		mu.Lock()
		seen = append(seen, e.Type)
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	for _, in := range []Input{
		{Type: "bidrl.deal_found", Source: "bidrl"},
		{Type: "core.job.failed", Source: SourceJobs},
		{Type: "bidrl.scan.started", Source: "bidrl"},
	} {
		if _, err := l.Publish(ctx, in); err != nil {
			t.Fatal(err)
		}
	}

	waitFor(t, "live delivery", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) == 2
	})
	mu.Lock()
	defer mu.Unlock()
	if seen[0] != "bidrl.deal_found" || seen[1] != "bidrl.scan.started" {
		t.Fatalf("delivered %v", seen)
	}
}

func TestDurableDeliversInOrderAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	_, l := newLog(t, nil)
	startLog(t, l)

	var mu sync.Mutex
	var got []int64
	cfg := DurableConfig{Name: "test.consumer", Pattern: "core.**",
		Start: CursorStart{Mode: FromBeginning}}

	sub, err := l.SubscribeDurable(cfg, func(_ context.Context, e Event) error {
		mu.Lock()
		got = append(got, e.ID)
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var want []int64
	for range 5 {
		id, err := l.Publish(ctx, Input{Type: "core.job.enqueued", Source: SourceJobs})
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, id)
		// Interleave non-matching rows; they must advance the cursor without delivery.
		if _, err := l.Publish(ctx, Input{Type: "bidrl.deal_found", Source: "bidrl"}); err != nil {
			t.Fatal(err)
		}
	}

	waitFor(t, "durable delivery", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == len(want)
	})
	mu.Lock()
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("delivery %d = %d, want %d (out of order)", i, got[i], want[i])
		}
	}
	mu.Unlock()
	sub.Close()

	// A restart resumes from the persisted cursor rather than replaying.
	var redelivered atomic.Int64
	sub2, err := l.SubscribeDurable(cfg, func(context.Context, Event) error {
		redelivered.Add(1)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub2.Close()

	newID, err := l.Publish(ctx, Input{Type: "core.job.failed", Source: SourceJobs})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "resumed delivery", func() bool { return redelivered.Load() >= 1 })
	if n := redelivered.Load(); n != 1 {
		t.Fatalf("resumed subscriber saw %d events, want only the new one (%d)", n, newID)
	}
}

func TestDurableStartFromNowSkipsHistory(t *testing.T) {
	ctx := context.Background()
	_, l := newLog(t, nil)
	startLog(t, l)

	for range 3 {
		if _, err := l.Publish(ctx, Input{Type: "core.job.enqueued", Source: SourceJobs}); err != nil {
			t.Fatal(err)
		}
	}

	var delivered atomic.Int64
	sub, err := l.SubscribeDurable(
		DurableConfig{Name: "from.now", Pattern: "core.**", Start: CursorStart{Mode: FromNow}},
		func(context.Context, Event) error { delivered.Add(1); return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	if _, err := l.Publish(ctx, Input{Type: "core.job.failed", Source: SourceJobs}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the new event", func() bool { return delivered.Load() >= 1 })
	time.Sleep(50 * time.Millisecond)
	if n := delivered.Load(); n != 1 {
		t.Fatalf("delivered %d events, want 1: history should not replay", n)
	}
}

func TestDurableRetriesThenPauses(t *testing.T) {
	ctx := context.Background()
	_, l := newLog(t, nil)
	startLog(t, l)

	var attempts atomic.Int64
	retry := RetryPolicy{MaxAttempts: 3, Initial: time.Millisecond, Maximum: 2 * time.Millisecond}
	sub, err := l.SubscribeDurable(
		DurableConfig{Name: "poison.consumer", Pattern: "core.**",
			Start: CursorStart{Mode: FromBeginning}, Retry: retry},
		func(context.Context, Event) error {
			attempts.Add(1)
			return errors.New("handler always fails")
		})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	poison, err := l.Publish(ctx, Input{Type: "core.job.failed", Source: SourceJobs})
	if err != nil {
		t.Fatal(err)
	}

	waitFor(t, "the subscription to pause", func() bool {
		subs, err := l.Subscribers(ctx)
		if err != nil {
			return false
		}
		for _, s := range subs {
			if s.Name == "poison.consumer" && s.State == CursorPaused {
				return true
			}
		}
		return false
	})

	subs, err := l.Subscribers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var status SubscriberStatus
	for _, s := range subs {
		if s.Name == "poison.consumer" {
			status = s
		}
	}
	if status.FailedEventID != poison {
		t.Errorf("failedEventId = %d, want %d", status.FailedEventID, poison)
	}
	if status.Attempts != retry.MaxAttempts {
		t.Errorf("attempts = %d, want %d", status.Attempts, retry.MaxAttempts)
	}
	if status.Healthy {
		t.Error("a paused subscriber must report unhealthy")
	}
	if n := attempts.Load(); n != int64(retry.MaxAttempts) {
		t.Errorf("handler ran %d times, want %d", n, retry.MaxAttempts)
	}

	// Pausing publishes core.event.subscription_paused for operational alerting.
	alerts, err := l.Query(ctx, Query{Pattern: TypeSubscriptionPaused})
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 1 {
		t.Fatalf("%d subscription_paused events, want 1", len(alerts))
	}
	if alerts[0].Subject != "poison.consumer" {
		t.Errorf("alert subject = %q", alerts[0].Subject)
	}

	// A later event must not pass the poison event silently.
	later, err := l.Publish(ctx, Input{Type: "core.job.succeeded", Source: SourceJobs})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	subs, _ = l.Subscribers(ctx)
	for _, s := range subs {
		if s.Name == "poison.consumer" && s.LastEventID >= later {
			t.Fatal("a paused subscriber advanced past the poison event")
		}
	}

	// Skipping resumes past it.
	if err := l.SkipPaused(ctx, "poison.consumer"); err != nil {
		t.Fatal(err)
	}
	subs, _ = l.Subscribers(ctx)
	for _, s := range subs {
		if s.Name == "poison.consumer" {
			if s.State != CursorActive {
				t.Errorf("state = %q after skip", s.State)
			}
			if s.LastEventID != poison {
				t.Errorf("lastEventId = %d after skip, want %d", s.LastEventID, poison)
			}
		}
	}
}

func TestDurableTxRollsBackHandlerWritesWithTheCursor(t *testing.T) {
	ctx := context.Background()
	store, l := newLog(t, nil)
	startLog(t, l)

	if err := store.Apply("testsink", []storage.Migration{
		{Version: 1, Name: "sink", Up: `CREATE TABLE testsink_rows(event_id INTEGER PRIMARY KEY) STRICT;`},
	}); err != nil {
		t.Fatal(err)
	}

	var fail atomic.Bool
	fail.Store(true)
	retry := RetryPolicy{MaxAttempts: 100, Initial: time.Millisecond, Maximum: time.Millisecond}

	sub, err := l.SubscribeDurableTx(
		DurableConfig{Name: "tx.consumer", Pattern: "core.**",
			Start: CursorStart{Mode: FromBeginning}, Retry: retry},
		func(ctx context.Context, tx storage.Tx, e Event) error {
			if _, err := tx.Exec(ctx, `INSERT INTO testsink_rows(event_id) VALUES (?)`, e.ID); err != nil {
				return err
			}
			if fail.Load() {
				return errors.New("side effect must roll back with the cursor")
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	id, err := l.Publish(ctx, Input{Type: "core.job.failed", Source: SourceJobs})
	if err != nil {
		t.Fatal(err)
	}

	// While the handler fails, neither its write nor the cursor may commit.
	time.Sleep(80 * time.Millisecond)
	var n int
	if err := store.QueryRow(ctx, `SELECT count(*) FROM testsink_rows`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d rows survived a failed transactional handler, want 0", n)
	}

	fail.Store(false)
	waitFor(t, "the handler to commit", func() bool {
		var n int
		if err := store.QueryRow(ctx, `SELECT count(*) FROM testsink_rows`).Scan(&n); err != nil {
			return false
		}
		return n == 1
	})

	var stored int64
	if err := store.QueryRow(ctx, `SELECT event_id FROM testsink_rows`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != id {
		t.Fatalf("stored event %d, want %d", stored, id)
	}
}

func TestDurableRejectsAPatternChange(t *testing.T) {
	_, l := newLog(t, nil)

	cfg := DurableConfig{Name: "stable.name", Pattern: "core.**"}
	sub, err := l.SubscribeDurable(cfg, func(context.Context, Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	sub.Close()

	cfg.Pattern = "bidrl.**"
	if _, err := l.SubscribeDurable(cfg, func(context.Context, Event) error { return nil }); !errors.Is(err, ErrPatternChanged) {
		t.Fatalf("got %v, want ErrPatternChanged", err)
	}
}

func TestDurableLeaseAdmitsOneDispatcher(t *testing.T) {
	ctx := context.Background()
	store, l := newLog(t, nil)
	startLog(t, l)

	// A second Log over the same database stands in for another process.
	other, err := New(store, store, Options{
		PollInterval: 10 * time.Millisecond,
		Owner:        "other-process",
		LeaseTTL:     time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	octx, ocancel := context.WithCancel(context.Background())
	other.Start(octx)
	defer func() { ocancel(); other.Stop() }()

	cfg := DurableConfig{Name: "leased.consumer", Pattern: "core.**",
		Start: CursorStart{Mode: FromBeginning}}

	var a, b atomic.Int64
	subA, err := l.SubscribeDurable(cfg, func(context.Context, Event) error { a.Add(1); return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer subA.Close()

	subB, err := other.SubscribeDurable(cfg, func(context.Context, Event) error { b.Add(1); return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer subB.Close()

	const n = 10
	for range n {
		if _, err := l.Publish(ctx, Input{Type: "core.job.enqueued", Source: SourceJobs}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "all events delivered once", func() bool { return a.Load()+b.Load() >= n })
	time.Sleep(60 * time.Millisecond)

	if total := a.Load() + b.Load(); total != n {
		t.Fatalf("delivered %d times for %d events; the lease did not hold", total, n)
	}
	if a.Load() != 0 && b.Load() != 0 {
		t.Fatalf("both dispatchers ran (a=%d b=%d); only one may hold the lease", a.Load(), b.Load())
	}
}

func TestRetentionRespectsAgeAndCursorPins(t *testing.T) {
	ctx := context.Background()
	store, l := newLog(t, func(o *Options) { o.Retention = time.Hour })

	// Three old events and one recent one.
	var ids []int64
	for range 3 {
		id, err := l.Publish(ctx, Input{Type: "core.job.enqueued", Source: SourceJobs})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	old := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339Nano)
	if _, err := store.Exec(ctx, `UPDATE core_events SET created_at = ?`, old); err != nil {
		t.Fatal(err)
	}
	recent, err := l.Publish(ctx, Input{Type: "core.job.failed", Source: SourceJobs})
	if err != nil {
		t.Fatal(err)
	}

	// A cursor stalled at the first event pins everything above it.
	sub, err := l.SubscribeDurable(
		DurableConfig{Name: "stalled", Pattern: "core.**", Start: CursorStart{Mode: FromBeginning}},
		func(context.Context, Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	sub.Close() // never dispatched: last_event_id stays 0

	rep, err := l.RunRetention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Deleted != 0 {
		t.Fatalf("deleted %d rows while a cursor pinned them", rep.Deleted)
	}
	if rep.PinnedBy != "stalled" || rep.PinnedRows != 3 {
		t.Fatalf("expected disk pressure from the stalled cursor, got %+v", rep)
	}

	// Removing the subscriber explicitly releases the pin.
	if err := l.RemoveSubscriber(ctx, "stalled"); err != nil {
		t.Fatal(err)
	}
	rep, err = l.RunRetention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Deleted != 3 {
		t.Fatalf("deleted %d rows, want 3", rep.Deleted)
	}
	if rep.OldestRetainedID != recent {
		t.Fatalf("oldestRetainedId = %d, want %d", rep.OldestRetainedID, recent)
	}

	remaining, err := l.Query(ctx, Query{Pattern: "**"})
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].ID != recent {
		t.Fatalf("retention removed the wrong rows: %+v (deleted %v)", remaining, ids)
	}

	got, err := l.OldestRetainedID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != recent {
		t.Fatalf("OldestRetainedID = %d, want %d", got, recent)
	}
}

func TestResetCursorReplaysFromTheBeginning(t *testing.T) {
	ctx := context.Background()
	_, l := newLog(t, nil)
	startLog(t, l)

	var delivered atomic.Int64
	cfg := DurableConfig{Name: "resettable", Pattern: "core.**", Start: CursorStart{Mode: FromNow}}
	sub, err := l.SubscribeDurable(cfg, func(context.Context, Event) error {
		delivered.Add(1)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	for range 3 {
		if _, err := l.Publish(ctx, Input{Type: "core.job.enqueued", Source: SourceJobs}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "initial delivery", func() bool { return delivered.Load() == 3 })

	if err := l.ResetCursor(ctx, "resettable", CursorStart{Mode: FromBeginning}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "replay after reset", func() bool { return delivered.Load() == 6 })
}
