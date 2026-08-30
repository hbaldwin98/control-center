# policy

**Layer 2** · `internal/core/policy` · imports `storage`, `events` · used by `ai`, `jobs`, `pluginhost`

**Responsibility.** Own plugin enabled state, budgets, and atomic spend reservations, and
answer whether a plugin may start work or a paid call.

---

## Interface

```go
package policy

// All money is integer micro-USD. Negative values are invalid at every boundary.
type MicroUSD int64

type Gate interface {
    CheckWork(ctx context.Context, pluginID string) error
    CheckWorkTx(ctx context.Context, tx storage.Tx, pluginID string) error

    // ReserveSpendTx admits one paid call inside the transaction that creates its
    // pending call record.
    ReserveSpendTx(ctx context.Context, tx storage.Tx, pluginID string, maximum MicroUSD) (Reservation, error)

    // SettleSpendTx replaces the reservation with actual committed cost inside the
    // caller's transaction. actual must be non-negative.
    SettleSpendTx(ctx context.Context, tx storage.Tx, reservationID string, actual MicroUSD) error

    // ReleaseSpendTx removes a reservation when the caller knows no charge occurred.
    ReleaseSpendTx(ctx context.Context, tx storage.Tx, reservationID string) error

    // Watch runs after an enabled-state commit. It is a cancellation signal, not a join.
    Watch(fn func(pluginID string, enabled bool)) func()
}

type Reservation struct {
    ID        string
    PluginID  string
    Maximum   MicroUSD
    AdmittedAt time.Time
}

type Admin interface {
    Register(ctx context.Context, pluginID string, automated bool) error
    Enable(ctx context.Context, pluginID, actor, reason string) error
    Disable(ctx context.Context, pluginID, actor, reason string) error
    SetBudget(ctx context.Context, pluginID string, b Budget) error
    State(ctx context.Context, pluginID string) (State, error)
    List(ctx context.Context) ([]State, error)
}

var (
    ErrPluginDisabled = errors.New("plugin disabled")
    ErrBudgetExceeded = errors.New("budget exceeded")
)
```

```go
type State struct {
    PluginID       string
    Enabled        bool
    Automated      bool
    DisabledAt     *time.Time
    DisabledBy     string
    DisabledReason string
    Budget         Budget
    ReservedHour   MicroUSD
    CommittedHour  MicroUSD
    ReservedDay    MicroUSD
    CommittedDay   MicroUSD
    ReservedMonth  MicroUSD
    CommittedMonth MicroUSD
}

type Budget struct {
    Hourly  MicroUSD // 0 = unlimited
    Daily   MicroUSD // 0 = unlimited
    Monthly MicroUSD // 0 = unlimited
    OnExceed ExceedAction
}

type ExceedAction string
const (
    ExceedReject  ExceedAction = "reject"
    ExceedDisable ExceedAction = "disable"
)
```

---

## Reservation Contract

The AI module computes a conservative maximum from the resolved provider, prices, input,
and output limits before making a provider request. The caller opens one SQLite write
transaction, inserts the pending call row, and invokes `ReserveSpendTx` to check enabled
state and every configured window and persist the reservation. Admission succeeds only
when this invariant remains true for each finite limit:

```
reserved + committed <= limit
```

Reservations pin the UTC hour, UTC day, and UTC calendar month containing admission.
Settlement deletes the reservation and increments committed cost in those same periods;
release deletes it without changing committed cost. A zero limit is unlimited and a
negative budget, maximum, or actual cost is rejected. Lowering a finite budget below
current reserved plus committed spend is also rejected.

Provider adapters must ensure that `actual` cannot exceed the reserved maximum. If that
invariant is nevertheless violated, settlement records the truthful actual charge,
atomically disables the plugin, emits `core.plugin.accounting_invariant_failed` and the
disabled event, and reports the AI route unhealthy. It emits `budget_exceeded` as well
only for each finite window the truthful charge actually crossed. Accounting must never
be rolled back merely because the cap was crossed.
Known uncharged failures release immediately. After restart, reservations for calls whose
outcome is unknowable are settled permanently at their reserved maximum rather than
silently undercounted.

The AI module performs these writes in one `storage.DB.Tx` callback:

1. Finalize `core_ai_calls` and all associated `core_ai_attempts` rows with integer
   micro-USD costs.
2. Call `SettleSpendTx` for the reservation and actual cost.
3. Call `events.PublishTx` for `core.ai.usage`.

Any error rolls back all three writes. This transaction-aware method is the only normal
settlement path.

---

## Enabled State

- `Register` validates the plugin ID and creates an unknown plugin disabled with reason
  `awaiting_configuration`; existing enabled state and budgets are never overwritten.
  Re-registration is idempotent. Changing `Automated` from false to true disables the
  plugin unless it already has a finite nonzero daily budget.
- An automated plugin cannot be enabled with an unlimited (`0`) daily budget. This keeps
  unattended work under a finite daily cap.
- `SetBudget` rejects an unlimited daily budget for an enabled automated plugin. The user
  must disable it first; changing a budget never silently bypasses the invariant.
- `Enable`, `Disable`, and an `ExceedDisable` transition write state and their required
  events in one transaction. Watchers run only after commit.
- Repeating an already-satisfied enable or disable is a no-op and does not emit a duplicate
  transition event.
- `Disable` closes new work and spend admission before it returns, then cancels registered
  contexts through `Watch`. It does not wait for arbitrary plugin code.
- A paid call whose reservation committed before disable is already in flight. It may
  finish and settle its charge. A competing reservation that commits after disable is
  rejected by the serialized state check.
- Disabling stops activity; it does not hide plugin tables or blobs.

Unknown plugin IDs are denied by every gate. When a reservation would exceed a window,
policy records
`core.plugin.budget_exceeded` once for that plugin, window, and period. `ExceedReject`
rejects the call. `ExceedDisable` also disables the plugin in the same transaction and
emits `core.plugin.disabled`. Policy commits that decision and its events before
`ReserveSpendTx` returns `ErrBudgetExceeded`; it does not roll back the event transaction by
returning the domain error from the transaction callback. `ReserveSpendTx` therefore
distinguishes technical failure from domain rejection: the caller commits a generated
budget/disable event, then returns `ErrBudgetExceeded` after the outer transaction.

---

## Tables

```
core_plugin_state(plugin_id, enabled, automated, disabled_at, disabled_by, disabled_reason)
core_plugin_budget(plugin_id, hourly_microusd, daily_microusd, monthly_microusd, on_exceed)
core_plugin_spend(plugin_id, window, period_start, committed_microusd)
core_plugin_spend_reservations(id, plugin_id, maximum_microusd, admitted_at,
                               hour_start, day_start, month_start)
core_plugin_budget_events(plugin_id, window, period_start, event_id)
```

The unique budget-event row prevents repeated rejected calls from flooding alerts. Spend
rollups and reservations are authoritative policy state; `core_ai_calls` and
`core_ai_attempts` remain the call-level audit record.
