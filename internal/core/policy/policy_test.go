package policy

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

type fixture struct {
	t     *testing.T
	store *storage.Store
	bus   *events.Log
	p     *Store
	now   time.Time
	mu    sync.Mutex
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	store, err := storage.Open(ctx, storage.Options{Path: filepath.Join(t.TempDir(), "policy.db")})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	bus, err := events.New(store, store, events.Options{})
	if err != nil {
		t.Fatalf("events: %v", err)
	}

	f := &fixture{t: t, store: store, bus: bus, now: time.Date(2026, 8, 30, 12, 30, 0, 0, time.UTC)}
	p, err := New(store, store, bus, f.clock)
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	f.p = p
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

// reserve runs a reservation the way ai does: budget rejections commit their events.
func (f *fixture) reserve(pluginID string, maximum MicroUSD) (Reservation, error) {
	f.t.Helper()
	ctx := context.Background()
	var res Reservation
	var rejected error

	err := f.store.Tx(ctx, func(tx storage.Tx) error {
		r, rerr := f.p.ReserveSpendTx(ctx, tx, pluginID, maximum)
		if IsDomainRejection(rerr) {
			rejected = rerr // its events are already in tx; commit them
			return nil
		}
		if rerr != nil {
			return rerr
		}
		res = r
		return nil
	})
	if err != nil {
		return Reservation{}, err
	}
	return res, rejected
}

func (f *fixture) settle(reservationID string, actual MicroUSD) error {
	ctx := context.Background()
	return f.store.Tx(ctx, func(tx storage.Tx) error {
		return f.p.SettleSpendTx(ctx, tx, reservationID, actual)
	})
}

func (f *fixture) release(reservationID string) error {
	ctx := context.Background()
	return f.store.Tx(ctx, func(tx storage.Tx) error {
		return f.p.ReleaseSpendTx(ctx, tx, reservationID)
	})
}

// eventTypes returns every published event type, in order.
func (f *fixture) eventTypes(pattern string) []string {
	f.t.Helper()
	found, err := f.bus.Query(context.Background(), events.Query{Pattern: pattern, Limit: 1000})
	if err != nil {
		f.t.Fatal(err)
	}
	out := make([]string, 0, len(found))
	for _, e := range found {
		out = append(out, e.Type)
	}
	return out
}

func (f *fixture) mustState(pluginID string) State {
	f.t.Helper()
	st, err := f.p.State(context.Background(), pluginID)
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

// enabled registers and enables a plugin with the given budget.
func (f *fixture) enabled(pluginID string, b Budget) {
	f.t.Helper()
	ctx := context.Background()
	if err := f.p.Register(ctx, pluginID, false); err != nil {
		f.t.Fatal(err)
	}
	if b != (Budget{}) {
		if err := f.p.SetBudget(ctx, pluginID, b); err != nil {
			f.t.Fatal(err)
		}
	}
	if err := f.p.Enable(ctx, pluginID, "test", "setup"); err != nil {
		f.t.Fatal(err)
	}
}

// ---- registration and enabled state ----

func TestRegisterCreatesDisabledAwaitingConfiguration(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if err := f.p.Register(ctx, "hello", false); err != nil {
		t.Fatal(err)
	}
	st := f.mustState("hello")
	if st.Enabled {
		t.Error("a newly registered plugin must start disabled")
	}
	if st.DisabledReason != ReasonAwaitingConfiguration {
		t.Errorf("reason = %q, want %q", st.DisabledReason, ReasonAwaitingConfiguration)
	}
	// Registration is not a transition, so it emits no enabled/disabled event.
	if got := f.eventTypes("core.plugin.**"); len(got) != 0 {
		t.Errorf("registration published %v", got)
	}
}

func TestRegisterIsIdempotentAndPreservesState(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.enabled("hello", Budget{Daily: 1000})
	for range 3 {
		if err := f.p.Register(ctx, "hello", false); err != nil {
			t.Fatal(err)
		}
	}
	st := f.mustState("hello")
	if !st.Enabled {
		t.Error("re-registration disabled an enabled plugin")
	}
	if st.Budget.Daily != 1000 {
		t.Errorf("re-registration overwrote the budget: %+v", st.Budget)
	}
}

func TestBecomingAutomatedWithoutADailyBudgetDisables(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.enabled("hello", Budget{}) // unlimited
	if err := f.p.Register(ctx, "hello", true); err != nil {
		t.Fatal(err)
	}
	st := f.mustState("hello")
	if st.Enabled {
		t.Error("becoming automated without a finite daily budget must disable the plugin")
	}
	if st.DisabledReason != ReasonAutomatedNoBudget {
		t.Errorf("reason = %q", st.DisabledReason)
	}

	// With a finite daily budget in place, the same transition is allowed to stand.
	f2 := newFixture(t)
	f2.enabled("hello", Budget{Daily: 5000})
	if err := f2.p.Register(context.Background(), "hello", true); err != nil {
		t.Fatal(err)
	}
	if !f2.mustState("hello").Enabled {
		t.Error("a finite daily budget should keep an automated plugin enabled")
	}
}

func TestAutomatedPluginCannotBeEnabledUncapped(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if err := f.p.Register(ctx, "hello", true); err != nil {
		t.Fatal(err)
	}
	if err := f.p.Enable(ctx, "hello", "test", "try"); !errors.Is(err, ErrAutomatedNeedsBudget) {
		t.Fatalf("got %v, want ErrAutomatedNeedsBudget", err)
	}
	if err := f.p.SetBudget(ctx, "hello", Budget{Daily: 1000}); err != nil {
		t.Fatal(err)
	}
	if err := f.p.Enable(ctx, "hello", "test", "now capped"); err != nil {
		t.Fatal(err)
	}
	// And the budget cannot then be widened back to unlimited while it is enabled.
	if err := f.p.SetBudget(ctx, "hello", Budget{Daily: 0}); !errors.Is(err, ErrAutomatedNeedsBudget) {
		t.Fatalf("got %v, want ErrAutomatedNeedsBudget", err)
	}
}

func TestTransitionsAreIdempotentAndEmitOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.enabled("hello", Budget{})
	for range 3 {
		if err := f.p.Enable(ctx, "hello", "test", "again"); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		if err := f.p.Disable(ctx, "hello", "test", "stop"); err != nil {
			t.Fatal(err)
		}
	}

	got := f.eventTypes("core.plugin.**")
	want := []string{events.TypePluginEnabled, events.TypePluginDisabled}
	if len(got) != len(want) {
		t.Fatalf("published %v, want exactly %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("published %v, want %v", got, want)
		}
	}
}

func TestDisableClosesAdmissionAndNotifiesWatchers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.enabled("hello", Budget{})

	type signal struct {
		plugin  string
		enabled bool
	}
	var mu sync.Mutex
	var seen []signal
	stop := f.p.Watch(func(pluginID string, enabled bool) {
		mu.Lock()
		seen = append(seen, signal{pluginID, enabled})
		mu.Unlock()
	})
	defer stop()

	if err := f.p.CheckWork(ctx, "hello"); err != nil {
		t.Fatalf("work should be admitted while enabled: %v", err)
	}
	if err := f.p.Disable(ctx, "hello", "operator", "kill switch"); err != nil {
		t.Fatal(err)
	}

	// Admission is closed by the time Disable returns.
	if err := f.p.CheckWork(ctx, "hello"); !errors.Is(err, ErrPluginDisabled) {
		t.Fatalf("got %v, want ErrPluginDisabled", err)
	}
	if _, err := f.reserve("hello", 100); !errors.Is(err, ErrPluginDisabled) {
		t.Fatalf("spend admission: got %v, want ErrPluginDisabled", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0].plugin != "hello" || seen[0].enabled {
		t.Fatalf("watchers saw %+v", seen)
	}
}

func TestEveryGateDeniesAnUnknownPlugin(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if err := f.p.CheckWork(ctx, "ghost"); !errors.Is(err, ErrPluginDisabled) {
		t.Errorf("CheckWork: got %v, want a denial", err)
	}
	if err := f.p.CheckWork(ctx, "ghost"); !errors.Is(err, ErrUnknownPlugin) {
		t.Errorf("CheckWork should name the cause: %v", err)
	}
	if _, err := f.reserve("ghost", 1); !errors.Is(err, ErrPluginDisabled) {
		t.Errorf("ReserveSpendTx: got %v, want a denial", err)
	}
}

func TestRegisterRejectsAnInvalidPluginID(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, id := range []string{"", "Hello", "1hello", "hello-world", "hello.world", "héllo"} {
		if err := f.p.Register(ctx, id, false); !errors.Is(err, ErrInvalidPluginID) {
			t.Errorf("Register(%q) = %v, want ErrInvalidPluginID", id, err)
		}
	}
}

// ---- reservations ----

func TestReserveSettleAndReleaseAreAtomic(t *testing.T) {
	f := newFixture(t)
	f.enabled("hello", Budget{Daily: 10_000})

	res, err := f.reserve("hello", 3_000)
	if err != nil {
		t.Fatal(err)
	}
	st := f.mustState("hello")
	if st.ReservedDay != 3_000 || st.CommittedDay != 0 {
		t.Fatalf("after reserve: reserved=%d committed=%d", st.ReservedDay, st.CommittedDay)
	}

	// Settling replaces the hold with the truthful charge in the same periods.
	if err := f.settle(res.ID, 1_250); err != nil {
		t.Fatal(err)
	}
	st = f.mustState("hello")
	if st.ReservedDay != 0 || st.CommittedDay != 1_250 {
		t.Fatalf("after settle: reserved=%d committed=%d", st.ReservedDay, st.CommittedDay)
	}
	if st.CommittedHour != 1_250 || st.CommittedMonth != 1_250 {
		t.Fatalf("settlement did not reach every window: %+v", st)
	}

	// Releasing drops the hold without charging.
	res2, err := f.reserve("hello", 4_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.release(res2.ID); err != nil {
		t.Fatal(err)
	}
	st = f.mustState("hello")
	if st.ReservedDay != 0 || st.CommittedDay != 1_250 {
		t.Fatalf("after release: reserved=%d committed=%d", st.ReservedDay, st.CommittedDay)
	}

	// A settled or released reservation is gone.
	if err := f.settle(res.ID, 1); !errors.Is(err, ErrUnknownReservation) {
		t.Fatalf("double settle: got %v, want ErrUnknownReservation", err)
	}
}

func TestConcurrentReservationsCannotConsumeTheSameCapacity(t *testing.T) {
	f := newFixture(t)
	f.enabled("hello", Budget{Daily: 1_000})

	// Three holds of 400 do not fit in 1000; the third must be rejected.
	if _, err := f.reserve("hello", 400); err != nil {
		t.Fatal(err)
	}
	if _, err := f.reserve("hello", 400); err != nil {
		t.Fatal(err)
	}
	if _, err := f.reserve("hello", 400); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("got %v, want ErrBudgetExceeded", err)
	}
	if st := f.mustState("hello"); st.ReservedDay != 800 {
		t.Fatalf("reserved = %d, want 800", st.ReservedDay)
	}
}

func TestReserveChecksEveryFiniteWindow(t *testing.T) {
	f := newFixture(t)
	f.enabled("hello", Budget{Hourly: 500, Daily: 100_000, Monthly: 0})

	if _, err := f.reserve("hello", 400); err != nil {
		t.Fatal(err)
	}
	// The daily and monthly limits allow it; the hourly one does not.
	if _, err := f.reserve("hello", 400); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("got %v, want the hourly limit to reject", err)
	}

	// A new hour has fresh hourly capacity, and the day carries over.
	f.advance(time.Hour)
	if _, err := f.reserve("hello", 400); err != nil {
		t.Fatalf("the next hour should have capacity: %v", err)
	}
	st := f.mustState("hello")
	if st.ReservedHour != 400 {
		t.Errorf("reservedHour = %d, want 400 in the new hour", st.ReservedHour)
	}
	if st.ReservedDay != 800 {
		t.Errorf("reservedDay = %d, want 800 across both hours", st.ReservedDay)
	}
}

func TestBudgetRejectionCommitsItsEventExactlyOncePerPeriod(t *testing.T) {
	f := newFixture(t)
	f.enabled("hello", Budget{Hourly: 100, OnExceed: ExceedReject})

	for range 5 {
		if _, err := f.reserve("hello", 500); !errors.Is(err, ErrBudgetExceeded) {
			t.Fatalf("got %v, want ErrBudgetExceeded", err)
		}
	}

	// The event survived the rejecting transaction, and repeated rejections do not flood.
	got := f.eventTypes(events.TypePluginBudgetExceeded)
	if len(got) != 1 {
		t.Fatalf("published %d budget_exceeded events, want exactly 1", len(got))
	}
	// ExceedReject leaves the plugin enabled.
	if !f.mustState("hello").Enabled {
		t.Error("ExceedReject must not disable the plugin")
	}

	// A new period alerts again.
	f.advance(time.Hour)
	if _, err := f.reserve("hello", 500); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatal(err)
	}
	if got := f.eventTypes(events.TypePluginBudgetExceeded); len(got) != 2 {
		t.Fatalf("published %d budget_exceeded events, want 2 across two periods", len(got))
	}
}

func TestExceedDisableNotifiesWatchersAfterCommit(t *testing.T) {
	f := newFixture(t)
	f.enabled("hello", Budget{Daily: 100, OnExceed: ExceedDisable})

	type signal struct {
		plugin  string
		enabled bool
	}
	var mu sync.Mutex
	var seen []signal
	stop := f.p.Watch(func(pluginID string, enabled bool) {
		mu.Lock()
		seen = append(seen, signal{pluginID, enabled})
		mu.Unlock()
	})
	defer stop()

	if _, err := f.reserve("hello", 500); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("got %v, want ErrBudgetExceeded", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0].plugin != "hello" || seen[0].enabled {
		t.Fatalf("watchers saw %+v after ExceedDisable committed", seen)
	}
}

func TestExceedDisableStopsThePluginInTheSameTransaction(t *testing.T) {
	f := newFixture(t)
	f.enabled("hello", Budget{Daily: 100, OnExceed: ExceedDisable})

	if _, err := f.reserve("hello", 500); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("got %v, want ErrBudgetExceeded", err)
	}

	st := f.mustState("hello")
	if st.Enabled {
		t.Fatal("ExceedDisable must disable the plugin")
	}
	if st.DisabledReason != ReasonBudgetExceeded {
		t.Errorf("reason = %q", st.DisabledReason)
	}

	got := f.eventTypes("core.plugin.**")
	want := []string{events.TypePluginEnabled, events.TypePluginBudgetExceeded, events.TypePluginDisabled}
	if len(got) != len(want) {
		t.Fatalf("published %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("published %v, want %v", got, want)
		}
	}
}

func TestAdmittedCallSurvivesAConcurrentDisable(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.enabled("hello", Budget{Daily: 10_000})

	// A reservation that committed before the disable is already in flight.
	res, err := f.reserve("hello", 2_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.p.Disable(ctx, "hello", "operator", "kill switch"); err != nil {
		t.Fatal(err)
	}

	// It may finish, and its charge is still recorded.
	if err := f.settle(res.ID, 1_800); err != nil {
		t.Fatalf("an admitted call must still settle: %v", err)
	}
	if st := f.mustState("hello"); st.CommittedDay != 1_800 {
		t.Fatalf("committed = %d, want the truthful charge", st.CommittedDay)
	}
	// A competing reservation after the disable is rejected.
	if _, err := f.reserve("hello", 1); !errors.Is(err, ErrPluginDisabled) {
		t.Fatalf("got %v, want ErrPluginDisabled", err)
	}
}

func TestNegativeAmountsAreRejected(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.enabled("hello", Budget{Daily: 1_000})

	if _, err := f.reserve("hello", -1); !errors.Is(err, ErrNegativeAmount) {
		t.Errorf("reserve: got %v, want ErrNegativeAmount", err)
	}
	res, err := f.reserve("hello", 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.settle(res.ID, -1); !errors.Is(err, ErrNegativeAmount) {
		t.Errorf("settle: got %v, want ErrNegativeAmount", err)
	}
	if err := f.p.SetBudget(ctx, "hello", Budget{Daily: -5}); !errors.Is(err, ErrNegativeAmount) {
		t.Errorf("budget: got %v, want ErrNegativeAmount", err)
	}
}

func TestLoweringABudgetBelowCurrentSpendIsRejected(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.enabled("hello", Budget{Daily: 10_000})

	res, err := f.reserve("hello", 3_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.p.SetBudget(ctx, "hello", Budget{Daily: 2_000}); !errors.Is(err, ErrBudgetBelowSpend) {
		t.Fatalf("got %v, want ErrBudgetBelowSpend while 3000 is reserved", err)
	}
	// Above the outstanding hold, it is allowed.
	if err := f.p.SetBudget(ctx, "hello", Budget{Daily: 5_000}); err != nil {
		t.Fatal(err)
	}

	if err := f.settle(res.ID, 3_000); err != nil {
		t.Fatal(err)
	}
	if err := f.p.SetBudget(ctx, "hello", Budget{Daily: 1_000}); !errors.Is(err, ErrBudgetBelowSpend) {
		t.Fatalf("got %v, want ErrBudgetBelowSpend while 3000 is committed", err)
	}
	// Unlimited is always allowed for a non-automated plugin.
	if err := f.p.SetBudget(ctx, "hello", Budget{}); err != nil {
		t.Fatalf("unlimited should be accepted: %v", err)
	}
}

func TestAccountingInvariantViolationRecordsTheTruthAndDisables(t *testing.T) {
	f := newFixture(t)
	f.enabled("hello", Budget{Daily: 10_000, Monthly: 0})

	res, err := f.reserve("hello", 1_000)
	if err != nil {
		t.Fatal(err)
	}
	// The adapter must make this impossible. Verify what happens when it is not.
	if err := f.settle(res.ID, 4_000); err != nil {
		t.Fatalf("settlement must not fail: %v", err)
	}

	st := f.mustState("hello")
	// Accounting is never rolled back merely because the cap was crossed.
	if st.CommittedDay != 4_000 {
		t.Fatalf("committed = %d, want the truthful 4000", st.CommittedDay)
	}
	if st.Enabled {
		t.Error("an accounting invariant failure must disable the plugin")
	}
	if st.DisabledReason != ReasonAccountingInvariant {
		t.Errorf("reason = %q", st.DisabledReason)
	}
	if st.AccountingFailed == nil {
		t.Error("state must report the accounting failure so the AI route reads unhealthy")
	}

	got := f.eventTypes("core.plugin.**")
	want := map[string]int{
		events.TypePluginEnabled:                   1,
		events.TypePluginAccountingInvariantFailed: 1,
		events.TypePluginDisabled:                  1,
	}
	counts := map[string]int{}
	for _, tpe := range got {
		counts[tpe]++
	}
	for tpe, n := range want {
		if counts[tpe] != n {
			t.Errorf("published %d %s, want %d (all: %v)", counts[tpe], tpe, n, got)
		}
	}
	// 4000 does not cross the 10000 daily limit, and monthly is unlimited, so no window
	// was actually crossed and no budget_exceeded is emitted.
	if counts[events.TypePluginBudgetExceeded] != 0 {
		t.Errorf("emitted budget_exceeded for a window the charge did not cross: %v", got)
	}
}

func TestInvariantViolationEmitsBudgetExceededOnlyForCrossedWindows(t *testing.T) {
	f := newFixture(t)
	f.enabled("hello", Budget{Hourly: 2_000, Daily: 100_000})

	res, err := f.reserve("hello", 1_000)
	if err != nil {
		t.Fatal(err)
	}
	// 5000 crosses the 2000 hourly limit but not the 100000 daily one.
	if err := f.settle(res.ID, 5_000); err != nil {
		t.Fatal(err)
	}

	found, err := f.bus.Query(context.Background(),
		events.Query{Pattern: events.TypePluginBudgetExceeded, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Fatalf("published %d budget_exceeded events, want exactly the crossed hourly window", len(found))
	}
	if got := string(found[0].Payload); !contains(got, `"window":"hour"`) {
		t.Fatalf("payload = %s, want the hourly window", got)
	}
}

func TestOrphanedReservationsSettleAtTheirMaximum(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.enabled("hello", Budget{Daily: 100_000})

	if _, err := f.reserve("hello", 2_500); err != nil {
		t.Fatal(err)
	}
	if _, err := f.reserve("hello", 1_500); err != nil {
		t.Fatal(err)
	}

	// A restart cannot know those outcomes, so they are counted at the most they could
	// have cost rather than silently undercounted.
	n, err := f.p.SettleOrphanedReservations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("settled %d reservations, want 2", n)
	}
	st := f.mustState("hello")
	if st.ReservedDay != 0 {
		t.Errorf("reserved = %d, want 0", st.ReservedDay)
	}
	if st.CommittedDay != 4_000 {
		t.Errorf("committed = %d, want 4000", st.CommittedDay)
	}
	if !st.Enabled {
		t.Error("settling at exactly the reserved maximum is not an invariant violation")
	}
}

func TestListReportsEveryRegisteredPlugin(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.enabled("alpha", Budget{Daily: 100})
	if err := f.p.Register(ctx, "beta", false); err != nil {
		t.Fatal(err)
	}

	all, err := f.p.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].PluginID != "alpha" || all[1].PluginID != "beta" {
		t.Fatalf("List = %+v", all)
	}
	if !all[0].Enabled || all[1].Enabled {
		t.Fatalf("unexpected enabled states: %+v", all)
	}
}

func TestPeriodStartPinsUTCBoundaries(t *testing.T) {
	at := time.Date(2026, 8, 30, 23, 45, 12, 0, time.UTC)
	if got := periodStart(WindowHour, at); !got.Equal(time.Date(2026, 8, 30, 23, 0, 0, 0, time.UTC)) {
		t.Errorf("hour = %v", got)
	}
	if got := periodStart(WindowDay, at); !got.Equal(time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("day = %v", got)
	}
	if got := periodStart(WindowMonth, at); !got.Equal(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("month = %v", got)
	}
	// A non-UTC instant pins the UTC period containing it, not the local one.
	local := time.Date(2026, 8, 30, 20, 0, 0, 0, time.FixedZone("UTC-8", -8*3600))
	if got := periodStart(WindowDay, local); !got.Equal(time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("day for a non-UTC instant = %v, want the UTC day", got)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
