# policy

**Layer 2** · `internal/core/policy` · imports `storage`, `events` · used by `ai`, `jobs`, `pluginhost`

**Responsibility.** Own plugin enabled state, budgets, and spend counters, and answer the
single question *may this plugin act right now?*

---

## Why this is its own module

`ai` and `jobs` both need to ask permission. `pluginhost` needs to grant and revoke it.
If the answer lived in `pluginhost`, the graph would cycle: `pluginhost → ai → pluginhost`.

Extracting it to L2 removes the cycle and puts every enforcement decision in one file that
can be read end to end. It is also the only place that needs testing to trust the kill
switch.

---

## Interface

Two faces: a hot path for capability modules, and an admin path for `pluginhost` and the UI.

```go
package policy

// Gate is the hot path. Called on every unit of work and every paid call.
type Gate interface {
    // CheckWork asks whether the plugin may start or continue work.
    CheckWork(ctx context.Context, pluginID string) error

    // CheckSpend asks whether the plugin may make a paid call.
    // Checked before the provider request, not after.
    CheckSpend(ctx context.Context, pluginID string, estimateUSD float64) error

    // Spend records actual cost and applies OnExceed if a window is now over.
    Spend(ctx context.Context, pluginID string, usd float64) error

    // Watch fires when a plugin's enabled state changes, so jobs can cancel
    // running work without polling.
    Watch(fn func(pluginID string, enabled bool)) func()
}

// Admin is the control path.
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
    PluginID      string
    Enabled       bool
    Automated     bool
    DisabledAt    *time.Time
    DisabledBy    string
    DisabledReason string
    Budget        Budget
    SpentThisHour float64
    SpentToday    float64
    SpentThisMonth float64
}

type Budget struct {
    HourlyUSD  float64 // 0 = unlimited
    DailyUSD   float64
    MonthlyUSD float64
    OnExceed   ExceedAction
}

type ExceedAction string
const (
    // ExceedReject fails the AI call; the plugin keeps running and can
    // degrade gracefully. Default.
    ExceedReject ExceedAction = "reject"

    // ExceedDisable disables the plugin outright, as if the switch were flipped.
    ExceedDisable ExceedAction = "disable"
)
```

---

## Rules

- **A plugin with `Automated: true` cannot be enabled without a non-zero daily budget.**
  `Enable` returns an error. This is the guardrail against a buggy scheduled job running
  all night.
- **Disable is synchronous from the caller's view.** `Disable` returns once state is
  written and watchers have been notified. Cancellation of in-flight work happens in
  `jobs`, driven by `Watch`.
- **Disabling stops activity; it does not hide data.** Plugin tables and blobs stay
  readable.

---

## Events emitted

| Event | When |
|---|---|
| `core.plugin.enabled` | after `Enable` succeeds |
| `core.plugin.disabled` | after `Disable` succeeds, or on `ExceedDisable` |
| `core.plugin.budget_exceeded` | a window limit is crossed |

`core.plugin.budget_exceeded` has a default notification rule.

---

## Tables

```
core_plugin_state(plugin_id, enabled, automated, disabled_at, disabled_by, disabled_reason)
core_plugin_budget(plugin_id, hourly_usd, daily_usd, monthly_usd, on_exceed)
core_plugin_spend(plugin_id, window, period_start, cost_usd)
```

`core_plugin_spend` is a rollup. It is reconstructible from `core_ai_usage`, and is kept
separate so the hot path is one indexed read rather than an aggregate.

---

## Notes

- `CheckSpend` takes an estimate rather than an exact cost because the cost is not known
  until the response arrives. The estimate need only be right enough to stop a runaway;
  `Spend` reconciles with the real figure.
- Windows are wall-clock in local time: hour, day, and calendar month.
