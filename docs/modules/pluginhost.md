# pluginhost

**Layer 4** · `internal/core/pluginhost` · imports all core modules · used by `web`, `cmd`

**Responsibility.** Register plugins, own validated non-secret plugin config, reconcile
persisted policy state, run lifecycle, and construct each scoped `Host`.

Enabled state and budgets live in [`policy`](policy.md). Pluginhost serializes lifecycle
changes and converges runtime resources to that persisted state.

---

## Interface

```go
package pluginhost

type Registry interface {
    RegisterAll(ps ...host.Plugin) error
    Start(ctx context.Context) error
    Stop(ctx context.Context) error

    Enable(ctx context.Context, id, actor, reason string) error
    Disable(ctx context.Context, id, actor, reason string) error

    Describe(id string) (Descriptor, error)
    List() []Descriptor
}

type Descriptor struct {
    Manifest host.Manifest
    State    policy.State
    Jobs     []string
    Routes   []RouteDescriptor
    Health   Health
}

type RouteDescriptor struct {
    Pattern string
}

type ConfigAdmin interface {
    GetConfig(ctx context.Context, pluginID string) (json.RawMessage, error)
    UpdateConfig(ctx context.Context, pluginID string, value json.RawMessage) error
}
```

`Health` distinguishes policy's desired state from the reconciled runtime state and
reports the last migration, init, route, scheduler, or subscription error. A partially
reconciled plugin is degraded, not silently reported as enabled.

---

## Registration

Registration is declarative and has no plugin side effects. It validates all plugins
before migrating or initializing any of them:

- plugin IDs match `[a-z][a-z0-9_]{0,62}`, are globally unique, and are not `core`
- job names and plugin-local durable subscription names are valid and unique per plugin;
  the host prefixes durable names with `plugin:<id>:` before registering them globally
- event patterns are valid segment globs, for example `bidrl.**`, and durable
  subscriptions have a name
- declared HTTP method/path pairs are relative to the plugin mount, canonical, and unique
- config schemas use the supported JSON Schema subset and validate their defaults
- declared AI model names are valid route names, unique per plugin, have a purpose, and
  use only known capabilities (`chat`, `vision`, `grounding`, `embed`)
- route, job, and subscription declarations do not collide

Declarations must be readable before `Init`; handlers are not invoked during
registration. Any collision or invalid declaration rejects `RegisterAll` as a whole.

Exactly one core file imports plugin packages:

```go
func registerPlugins(r pluginhost.Registry) error {
    return r.RegisterAll(hello.New(), pagewatch.New(), tid.New())
}
```

---

## Constructing the facade

```go
func (r *registry) facadeFor(m host.Manifest) host.Host {
    scopedEvents := events.Scoped(r.bus, m.ID)
    scopedStore := storage.Prefixed(r.db, m.ID)
    scopedBlobs := storage.Namespaced(r.blobs, m.ID)
    gatedBlobStore := storage.WithMutationAdmission(scopedBlobs,
        func(ctx context.Context, tx storage.Tx) error {
            return r.policy.CheckWorkTx(ctx, tx, m.ID)
        })

    return &scopedHost{
        pluginID: m.ID,
        ai:       ai.Scoped(r.ai, m.ID),
        browser:  browser.Scoped(r.browser, m.ID),
        jobs:     jobs.Scoped(r.jobs, m.ID),
        events:   gatedEvents{inner: scopedEvents, db: r.db, gate: r.policy, pluginID: m.ID},
        store:    gatedStore{inner: scopedStore, gate: r.policy, pluginID: m.ID},
        blobs:    gatedBlobStore,
        config:   r.config.For(m.ID),
        log:      r.log.With("plugin", m.ID),
        clock:    r.clock,
    }
}
```

The L4 `gatedEvents` wrapper owns the database handle: standalone `Publish` opens one
transaction, calls `CheckWorkTx`, and delegates to `PublishTx`; caller-supplied
`PublishTx` performs the same check in that transaction. `gatedStore` similarly joins the
check to SQL mutations. Storage's policy-agnostic `WithMutationAdmission` hook invokes
the supplied callback inside blob `Put`/`Delete` metadata transactions and discards a
staged file on rejection. This preserves the layer rule: L0 storage and L1 events do not
import L2 policy. Scoped wrappers stamp identity; L4 wrappers/callbacks enforce
admission. This is host-capability revocation, not a security sandbox. A plugin remains
ordinary in-process Go code and can retain goroutines, finish work admitted before
disable, or use direct networking.

---

## Startup and reconciliation

On startup, pluginhost registers declarations, creates missing policy rows without
overwriting persisted enabled state or budgets, runs every plugin's idempotent migrations,
reconciles config, loads persisted policy state, and reconciles each plugin:

```
enabled  → Init(host) → mount plugin routes → schedule jobs → invoke subscriptions
disabled → no Init    → mount host 503       → no schedules  → discard/ack durable events
```

Disabled plugins still migrate so their stored data remains readable and future enable
does not put schema work on the request path. They do not receive `Init` or any new
execution entrypoint. The route is owned by the host and always returns `503` without
calling plugin code, using the standard envelope with `code: "plugin_disabled"`.

Durable subscriptions for a disabled plugin remain attached in discard/ack mode: the
host advances their cursors without invoking handlers. This intentionally chooses "off
means ignore incoming work" over replaying a potentially large or costly backlog on
enable. Live subscriptions are detached because they have no cursor to preserve.

Startup continues when one plugin cannot reconcile. That plugin remains behind the host
503/discard boundary and reports degraded health; unrelated plugins can start. Migration
failure is also degraded and prevents that plugin's `Init`.

Config reconciliation validates manifest defaults first. A missing row is inserted from
those defaults. An existing row is validated against the current manifest schema before
`Init`; pluginhost never resets or coerces it silently. Invalid persisted config leaves
the plugin behind the host 503/discard boundary with degraded health even if policy's
desired state is enabled. The administrator can submit a valid replacement through
`ConfigAdmin`, after which normal reconciliation resumes.

---

## Enable and disable

Lifecycle reconciliation is serialized per plugin, idempotent, and restart-safe. Policy
state is the durable desired state; each step records runtime health and can be retried.
`Enable` and `Disable` return an error if convergence is incomplete, but they do not roll
back persisted policy state. Startup and the background reconciler resume unfinished
work.

Disable persists policy revocation first. Admission checks then reject new jobs, AI
dispatches, browser sessions, subscription handlers, plugin HTTP requests, plugin event
publication, and plugin storage/blob mutations. Reconciliation cancels admitted job, AI,
subscription, request, and browser contexts; closes admitted browser sessions; replaces
the route with the host 503; changes durable subscriptions to discard/ack; detaches live
subscriptions; and removes cron schedules. It then calls `Shutdown` with a bounded
context for a plugin that was initialized. A timeout leaves health degraded; Go cannot force the plugin's
goroutines to exit. Repeating any step has the same result.

Config watches belong to one enabled runtime generation. Disable cancels their callback
contexts and detaches them before `Shutdown`; updates while disabled are persisted but do
not invoke plugin code. The next successful `Init` reads the latest snapshot and may
register new watches.

Enable runs `Init` once for that enabled runtime generation, swaps in validated routes,
activates job schedules without cron catch-up, and replaces discard/ack subscriptions
with handlers at their current cursors. A crash between steps is safe because the next
reconciliation derives work from persisted policy state and current runtime state.

---

## Enforcement matrix

| Boundary | Behaviour when disabled | Enforced in |
|---|---|---|
| Cron scheduler | Tick skipped; no queue or later catch-up. | `jobs` |
| `Jobs().Enqueue` and worker claim | Rejected; claim rechecks policy. | `jobs` |
| Admitted jobs | Context cancelled; cooperative handler is fenced and recorded cancelled. | `jobs` |
| New `AI().Chat`, `ChatStream`, `Embed` | Rejected before reservation or provider dispatch. | `ai` |
| Admitted AI calls | Context cancelled; paid provider work may finish and is accounted. | `ai` |
| New `Browser().Open` / page I/O | Rejected before any engine call. | `browser` |
| Admitted browser sessions | Context cancelled; pages and the browser context are closed. | `browser` |
| New subscription deliveries | Handler not invoked; durable cursor advances in discard/ack mode. | `pluginhost` |
| Admitted subscription handlers | Context cancelled; handler may continue cooperatively. | `pluginhost` |
| New HTTP requests | Host-owned `503`; plugin handler not invoked. | `pluginhost` |
| Admitted HTTP requests | Request context cancelled; handler may continue cooperatively. | `pluginhost` |
| `Events().Publish` | New publication rejected; an admitted transaction may commit. | `events`, `pluginhost` |
| `Store()` / `Blobs()` mutations | New mutation rejected; an admitted transaction may commit. | `storage`, `pluginhost` |
| `Store()` / `Blobs()` reads | Host UI may read retained data. | `storage`, `web` |
| `Config()` / `Log()` / `Clock()` | Reads and diagnostic logging remain available. | `pluginhost` |

The host does not claim to kill arbitrary goroutines, block direct networking, or prevent
all writes through handles already held by plugin code. Strong isolation would require a
separate process and an RPC capability boundary.

---

## Tables

```
core_plugin_config(plugin_id, value_json, version, updated_at, updated_by)
```

`ConfigAdmin.UpdateConfig` derives the actor from authenticated context, validates the
manifest schema, commits the document, then notifies only the plugin's active enabled
runtime generation. It never accepts secret-valued schema fields.
