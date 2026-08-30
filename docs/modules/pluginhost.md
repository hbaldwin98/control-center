# pluginhost

**Layer 4** · `internal/core/pluginhost` · imports all core modules · used by `web`, `cmd`

**Responsibility.** Register plugins, run their lifecycle, and construct the scoped `Host`
each one receives.

It holds no policy of its own. Enabled state and budgets live in
[`policy`](policy.md); this module calls it.

---

## Interface

```go
package pluginhost

type Registry interface {
    RegisterAll(ps ...host.Plugin) error

    // Start migrates, initialises, mounts routes, schedules jobs, and
    // attaches subscriptions for every registered plugin.
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
    Routes   string  // mount path
    Health   Health
}
```

`Enable` and `Disable` delegate to `policy.Admin` and then reconcile: mount or unmount
routes, attach or detach subscriptions. The authoritative state is always `policy`'s.

---

## Constructing the facade

This is the load-bearing part of the module. A plugin never receives an unscoped service,
so it cannot forge an identity or bypass the gate.

```go
func (r *registry) facadeFor(m host.Manifest) host.Host {
    return &scopedHost{
        pluginID: m.ID,
        ai:       ai.Scoped(r.ai, m.ID),        // stamps plugin ID onto every ctx
        jobs:     jobs.Scoped(r.jobs, m.ID),
        events:   events.Scoped(r.bus, m.ID),   // forces Source = m.ID
        store:    storage.Prefixed(r.db, m.ID), // restricted to "<id>_" tables
        blobs:    storage.Namespaced(r.blobs, m.ID),
        config:   r.config.For(m.ID),
        log:      r.log.With("plugin", m.ID),
        clock:    r.clock,
    }
}
```

Every scoped wrapper does the same two things: stamp the identity, and consult
`policy.Gate` where relevant. That is the whole enforcement story.

When a plugin later moves out of process, `scopedHost` becomes an RPC client holding a
plugin token. The plugin's own code does not change.

---

## Lifecycle

```
RegisterAll  →  Migrate  →  Init(host)  →  read declarations  ┬→ routes mounted
                                                              ├→ jobs scheduled
                                                              └→ subscriptions attached
                                                                 → running

Disable  →  policy.Disable  →  scheduler skips  →  running jobs cancelled
         →  subscriptions detached  →  routes 503  →  AI gate rejects

Enable   →  the reverse, minus any replay of missed cron ticks
```

`Migrate` runs before `Init`, on every start, idempotently.

---

## The enforcement matrix

What "disabled" means, boundary by boundary. Each row is enforced by the host, never by
plugin code.

| Boundary | Behaviour when disabled | Enforced in |
|---|---|---|
| Cron scheduler | Tick skipped. Not queued, not backlogged. | `jobs` |
| `Jobs().Enqueue` | Rejected, `ErrPluginDisabled`. | `jobs` |
| Running jobs | Context cancelled; recorded `cancelled`, reason `plugin_disabled`. | `jobs` |
| `AI().Chat` / `ChatStream` / `Embed` | Rejected before any provider call. | `ai` |
| Event subscriptions | Handlers not invoked. Durable cursors still advance, so re-enabling does not replay a backlog. | `pluginhost` |
| HTTP routes | `503`, body naming the plugin. | `pluginhost` |
| `Store()` / `Blobs()` | Still readable. Disabling stops activity, it does not hide data. | — |

The `hello` plugin's acceptance test exercises every row.

---

## Registration

Exactly one file in the core app imports plugin packages:

```go
// cmd/controlcenter/plugins.go
package main

func registerPlugins(r pluginhost.Registry) error {
    return r.RegisterAll(
        hello.New(),
        bidrl.New(),
    )
}
```

Everything else about the plugin contract is in [`docs/plugin-api.md`](../plugin-api.md).
