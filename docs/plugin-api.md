# Plugin API

Everything needed to write a plugin. Self-contained — you should not need to read the core
module specs to build one.

A plugin is a Go module that implements one interface, declares what it wants to run, and
talks to the rest of the system through a single `Host` handle.

---

## 1. The shape of a plugin

```
plugins/bidrl/
  go.mod            depends on host, and nothing else from this project
  plugin.go         manifest + wiring
  migrations.go
  jobs.go
  ui/               React components, mounted by the shell
```

Your `go.mod` depends on `github.com/hbaldwin98/control-center/host`. Core internals live
under `internal/`, so importing them is a build error rather than a review comment. That
is deliberate: it keeps the option of running plugins out of process open.

---

## 2. The `Plugin` interface

```go
package host

type Plugin interface {
    Manifest() Manifest

    // Migrate runs before Init, on every start, idempotently.
    Migrate(m Migrator) error

    // Init receives a Host already scoped to this plugin.
    Init(ctx context.Context, h Host) error

    // Declarations, read once after Init returns.
    Jobs() []jobs.Def
    Subscriptions() []Subscription
    Routes() http.Handler   // mounted at /api/plugins/<id>/

    Shutdown(ctx context.Context) error
}
```

### Manifest

```go
type Manifest struct {
    ID          string   // stable, lowercase; also the table and blob prefix
    Name        string
    Version     string
    Description string

    // Automated marks plugins that act without the user initiating —
    // cron jobs, or event handlers that spend money. The UI surfaces the
    // kill switch prominently for these, and the plugin cannot be enabled
    // until a daily budget is set.
    Automated bool

    UI UIManifest
}

type UIManifest struct {
    Entry string      // frontend module path, e.g. "plugins/bidrl"
    Icon  string
    Nav   []NavItem
}
```

Set `Automated: true` honestly. It is the flag that forces you to give the plugin a budget
before it can run unattended, which is the guardrail against a bug spending all night.

### Subscriptions

```go
type Subscription struct {
    Pattern string          // "bidrl.*", "core.job.dead", "**"
    Durable bool            // cursor-tracked, replayed on restart
    Name    string          // required when Durable
    Handler events.Handler
}
```

---

## 3. The `Host` handle

This is the entire surface available to you.

```go
type Host interface {
    PluginID() string

    AI() ai.AI               // the only way to spend money
    Jobs() jobs.Jobs         // enqueue, cancel, inspect
    Events() Events          // publish; Source is forced to your plugin ID
    Store() storage.DB       // SQL, restricted to your table prefix
    Blobs() storage.Blobs    // binary storage, namespaced to you
    Config() Config          // user-editable settings, rendered in the UI
    Log() Logger             // structured, tagged with plugin and job
    Clock() Clock            // injectable, so scheduled logic is testable
}
```

### What is deliberately missing

| Missing | Why | Do this instead |
|---|---|---|
| Credentials, API keys, provider names | You must never hold a provider token; the spend gate lives inside `AI()`. | Ask for a logical model: `"cheap-vision"`. |
| A notifications API | Preserves the dependency direction — nothing calls notifications. | Publish an event. See §6. |
| Raw `*sql.DB` | Table-prefix guardrail, and the seam that lets a plugin move out of process. | Use `Store()`. |
| Anything belonging to another plugin | Plugins compose through events, not imports. | Subscribe to their events. |

Network access, HTTP clients, and browser automation are **not** provided and **not**
restricted. If your plugin needs Playwright, it owns that dependency. One consumer is not
enough information to design a shared API against; when a second plugin wants a browser,
it becomes a capability.

---

## 4. Using AI

```go
resp, err := h.AI().Chat(ctx, ai.ChatRequest{
    Model:  "cheap-vision",          // logical name; you never learn the provider
    Schema: lotAnalysisSchema,       // structured output
    Messages: []ai.Message{{
        Role: ai.RoleUser,
        Text: prompt,
        Images: []ai.Image{
            {Blob: photo1, MIME: "image/jpeg", Resolution: ai.ResolutionMedium},
            {Blob: label,  MIME: "image/jpeg", Resolution: ai.ResolutionHigh},
        },
    }},
})
```

Points that matter in practice:

- **Send related images in one request.** The model correlates them; separate calls cannot.
- **`Resolution` is your main cost lever.** Medium for everything, high on retry for the
  photo with the unreadable label.
- **Cost is recorded for you.** Never track your own spend; the host attributes every call
  to your plugin and, when inside a job, to that job.
- **Two errors you must handle:** `policy.ErrPluginDisabled` and `policy.ErrBudgetExceeded`.
  Both mean stop cleanly, not retry.

---

## 5. Jobs

```go
func (p *Plugin) Jobs() []jobs.Def {
    return []jobs.Def{{
        Name:        "scan",
        Schedule:    "0 3 * * *",      // empty means enqueue-only
        Timeout:     2 * time.Hour,
        MaxAttempts: 2,
        Concurrency: 1,
        Handler:     p.scan,
    }}
}

func (p *Plugin) scan(jc jobs.Context) error {
    var args ScanArgs
    if err := jc.Args(&args); err != nil {
        return err
    }

    for i, lot := range lots {
        if err := jc.Err(); err != nil {
            return err              // cancelled: stop promptly
        }
        jc.Progress(float64(i)/float64(len(lots)), lot.Title)
        jc.Logf("analyzing lot %s", lot.ID)
        ...
    }
    return nil
}
```

**`jobs.Context` is a `context.Context`.** It is cancelled on stop, on timeout, and when
your plugin is disabled. Check it in loops. A handler that ignores cancellation is killed
at its timeout — and cannot spend anything meanwhile, because `AI()` checks the gate
independently.

Use `WithIdempotencyKey` when enqueuing something that must not double-run:

```go
h.Jobs().Enqueue(ctx, "scan", args, jobs.WithIdempotencyKey("auction-"+id))
```

---

## 6. Events, and how to reach the user

Publish domain events. Your plugin ID is stamped as the source automatically.

```go
h.Events().Publish(ctx, "deal_found", lot.ID, DealFound{
    LotID: lot.ID, Title: lot.Title, Bid: lot.Bid, Est: est,
})
// becomes: bidrl.deal_found
```

**To notify the user, publish an event — do not look for a notify API.** The user writes a
rule against your event type. For the case where you genuinely want to reach them without
any configuration, publish `<plugin>.alert`, which has a default rule:

```go
h.Events().Publish(ctx, "alert", auctionID, Alert{
    Title: "Auction closes in 30 minutes",
    Body:  "6 watched lots still under estimate",
})
```

Naming convention is **`<noun>.<past-tense-verb>`** — `lot.analyzed`, `deal_found`,
`scan.completed`. Consistency is what makes rules writable later.

---

## 7. Storage

```go
func (p *Plugin) Migrate(m host.Migrator) error {
    return m.Apply([]host.Migration{
        {Version: 1, Name: "lots", Up: `
            CREATE TABLE bidrl_lots (
                id TEXT PRIMARY KEY,
                auction_id TEXT NOT NULL,
                title TEXT NOT NULL,
                current_bid_cents INTEGER,
                analyzed_at TIMESTAMP
            );`},
    })
}
```

Every table must start with your plugin ID and an underscore. Migrations that create
tables outside your prefix are rejected. Blob keys are namespaced the same way.

---

## 8. The `hello` plugin

Written before any real plugin. Its purpose is to prove the host API is usable and to be
the integration test for the kill switch. Build it at milestone 7 and do not skip it — it
is where the host API gets fixed while fixing it is still cheap.

It exercises every host capability and nothing else:

| Capability | What `hello` does |
|---|---|
| Jobs | a cron job every minute |
| AI | one tiny `Chat` call, so cost attribution has a live source |
| Events | publishes `hello.ticked` |
| Subscriptions | a durable subscription to its own event |
| Store | one table recording ticks |
| Blobs | writes and reads one small blob |
| UI | one page listing its history |

### Acceptance test

Disabling `hello` while its job is mid-flight must:

1. cancel the running job, recorded as `cancelled` / `plugin_disabled`
2. skip the next cron tick, with no backlog on re-enable
3. reject a straggler `AI().Chat` with `ErrPluginDisabled`, with no provider call made
4. produce no further `core_ai_usage` rows
5. return `503` from `/api/plugins/hello/*`
6. leave `hello_ticks` readable

Every row of the enforcement matrix, tested by something that is not your real plugin.

---

## 9. Registration

One file in the core app, and one line in it:

```go
// cmd/controlcenter/plugins.go
func registerPlugins(r pluginhost.Registry) error {
    return r.RegisterAll(
        hello.New(),
        bidrl.New(),
    )
}
```

Frontend registration mirrors this exactly — see [`frontend.md`](frontend.md).
