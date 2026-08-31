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

web/src/plugins/bidrl/
  index.tsx         canonical frontend module: nav, routes, icons, components
```

Your `go.mod` depends on `github.com/hbaldwin98/control-center/host`. CI runs an
architectural dependency test over each plugin's complete `go list -deps` output and
rejects imports from `internal/core`, another plugin, or the application module outside
`host`; `go.work` and Go's `internal` rule are not sufficient by themselves. This keeps
out-of-process plugins as a compatible direction, but that move will require protocols,
adapters, supervision, and capability proxies; it is not just a transport or build swap.

The public module contains `host` plus `host/ai`, `host/browser`, `host/events`,
`host/jobs`, `host/policy`, and `host/storage`. Those packages contain only the
interfaces, DTOs, options, and sentinel errors shown here; they do not import
`internal/core`. Core modules adapt their internal implementations to these public
contracts. Plugin code never imports an `internal/` package.

---

## 2. The `Plugin` interface

```go
package host

type Plugin interface {
    Manifest() Manifest

    // Declarations are pure and are read during registration, before Migrate or Init.
    Jobs() []jobs.Def
    Subscriptions() []Subscription
    Routes() []Route

    // Migrate runs before Init, on every start, idempotently.
    Migrate(m Migrator) error

    // Init receives a Host already scoped to this plugin.
    Init(ctx context.Context, h Host) error

    Shutdown(ctx context.Context) error
}

type Route struct {
    Pattern string       // Go 1.22 relative pattern, for example "POST /scans"
    Handler http.Handler
}
```

### Manifest

```go
type Manifest struct {
    ID          string   // stable `[a-z][a-z0-9_]{0,62}`; `core` is reserved
    Name        string
    Version     string
    Description string

    // Automated is true only when work can start without an explicit user action,
    // such as cron or an event handler. Such plugins require a daily budget.
    Automated bool

    Config ConfigSpec
}

type ConfigSpec struct {
    Schema   json.RawMessage // supported JSON Schema 2020-12 subset
    Defaults json.RawMessage
}
```

Set `Automated: true` honestly. It is the flag that forces you to give the plugin a budget
before it can run unattended, which is the guardrail against a bug spending all night.
The manifest owns identity and runtime metadata only. The `PluginModule` at
`web/src/plugins/<id>` owns routes, navigation, and icons. At build/startup the shell
checks frontend IDs against backend descriptors and rejects missing IDs, duplicates, and
route or navigation collisions.

Declarations must not depend on `Init`; the host needs them to validate registration and
advance disabled plugins' durable cursors without executing plugin code. Route patterns
are mounted below `/api/plugins/<id>/`, may not escape that prefix, and are validated for
method/path collisions before any plugin migrates. Handlers receive the plugin-relative
path beginning with `/`; authentication, admission, panic recovery, request-size limits,
common security headers, and mutation CSRF checks run before the handler.

### Subscriptions

```go
type Subscription struct {
    Pattern string          // "bidrl.**", "core.job.dead", "**.failed"
    Handler events.Handler  // live, lossy delivery
    Durable *DurableSubscription
}

type DurableSubscription struct {
    Name    string           // stable and unique within this plugin
    Handler events.TxHandler // receives the transaction that advances the cursor
}
```

Set `Handler` for live delivery or `Durable` for durable delivery, never both.

Event types and patterns are dot-separated segments. `*` matches exactly one segment;
`**` matches zero or more. Therefore `bidrl.*` matches `bidrl.alert` but not
`bidrl.lot.analyzed`; use `bidrl.**` for every BIDRL event and `**.failed` for any event
whose last segment is `failed`. Durable subscriptions invoke one handler at a time in
event-ID order and provide at-least-once delivery. Handler writes and the cursor commit in
the supplied transaction; retries can still repeat external side effects, so handlers
must be idempotent. A disabled plugin receives no handler calls; its durable cursor is
advanced in discard/ack mode to prevent a backlog on re-enable.
Plugin durable names are registered globally as `plugin:<id>:<name>`. A new declaration
starts `FromNow` and uses the standard retry policy; an existing name always resumes its
persisted cursor. V1 does not expose cursor resets or custom retry policy to plugin code.

---

## 3. The `Host` handle

This is the entire surface available to you.

```go
type Host interface {
    PluginID() string

    AI() ai.AI               // the only host-managed paid provider path
    Browser() browser.Browser // headless sessions; the host owns the engine
    Jobs() jobs.Jobs         // enqueue, cancel, inspect
    Events() Events          // publish; Source is forced to your plugin ID
    Store() storage.DB       // SQL, restricted to your table prefix
    Blobs() storage.Blobs    // binary storage, namespaced to you
    Config() Config          // user-editable settings, rendered in the UI
    Log() Logger             // structured, tagged with plugin and job
    Clock() Clock            // injectable, so scheduled logic is testable
}
```

`ConfigSpec.Schema` uses a closed JSON Schema 2020-12 subset: objects, arrays, strings,
numbers, integers, booleans, `enum`, bounds, patterns, required properties, descriptions,
and defaults. Remote references, custom code, and secret-valued fields are forbidden.
The host validates defaults and every update, stores one JSON document per plugin, and
renders the supported controls on the Plugins screen. Secrets are credential references, never
config values.

```go
type Config interface {
    Decode(dst any) error
    Watch(fn func(context.Context, json.RawMessage)) func()
}
```

Plugins read their current snapshot and may watch later updates; only the authenticated
web administration API writes config. Updates are atomic. A callback failure is logged
but does not roll back a committed value, and callbacks never run under the config lock.
Watch callbacks and their contexts belong to the current enabled runtime generation;
disable cancels and detaches them. Updates while disabled are visible to the next `Init`
but invoke no plugin code.

### What is deliberately missing

| Missing | Why | Do this instead |
|---|---|---|
| Credentials, API keys, provider selection | You must never hold a provider token or select an AI provider; the spend gate lives inside `AI()`. Operational core events may name a configured provider. | Ask for a logical model: `"cheap-vision"`. |
| A notifications API | Preserves the dependency direction — nothing calls notifications. | Publish an event. See §6. |
| Raw `*sql.DB` | Table-prefix guardrail, and the seam that lets a plugin move out of process. | Use `Store()`. |
| Playwright, chromedp, or a raw CDP handle | The kill switch cannot close a browser the plugin launched. SSRF checks live in the host. | `h.Browser().Open` with an allowlist. See [`browser.md`](modules/browser.md). |
| Anything belonging to another plugin | Plugins compose through events, not imports. | Subscribe to their events. |

Ordinary `net/http` for APIs is still the plugin's own. The host-managed browser is the
path for JavaScript-rendered pages and for fetches that must share a cookie jar and an
HTTPS host allowlist.

The plugin switch is a **host-capability kill switch**. Once disabled, the host rejects
new managed jobs, AI dispatches, browser sessions, event-handler invocations, and requests
to the plugin's HTTP routes, plus new event publications and storage/blob mutations
through the facade. Admitted browser sessions are closed, not asked to finish. Reads and
diagnostic logging remain available. The host cancels contexts it already admitted, but
an admitted transaction may commit. In-process trusted code can ignore cancellation or
use direct networking. Hard termination and network containment require out-of-process
isolation.

The host authenticates every request under `/api/plugins/<id>/`, checks plugin admission,
and enforces `Origin` plus synchronizer-token CSRF checks before invoking a mutating route.
Handlers return domain data or the common JSON error envelope
`{"error":{"code":"...","message":"..."}}`; the frontend `@cc/ui` client handles
the prefix, credentials, CSRF header, envelope, and disabled-plugin `503` response.
The host-owned disabled response uses HTTP `503` and code `plugin_disabled`; clients
classify the code rather than treating every `503` as a disabled plugin.

---

## 4. Using AI

```go
resp, err := h.AI().Chat(ctx, ai.ChatRequest{
    Model:  "cheap-vision",          // logical name; you do not select the provider
    Schema: lotAnalysisSchema,       // structured output
    Messages: []ai.Message{{
        Role: ai.RoleUser,
        Text: prompt,
        Images: []ai.Image{
            {Blob: photo1, MIME: "image/jpeg", Resolution: ai.ResolutionMedium},
            {Blob: label,  MIME: "image/jpeg", Resolution: ai.ResolutionHigh},
        },
    }},
    Grounding: &ai.GroundingOptions{
        MaxQueries:     4,
        Freshness:      30 * 24 * time.Hour,
        AllowedDomains: []string{"example-market.test"},
    },
})
```

Grounded responses carry inspectable evidence rather than a boolean assertion:

```go
type GroundingOptions struct {
    MaxQueries     int           // required and bounded by the logical route
    Freshness      time.Duration // zero means no freshness constraint
    AllowedDomains []string      // empty means unrestricted
}

type Citation struct {
    Start, End int // byte offsets in Text
    Source     int // index into Sources
}

type Source struct {
    URL         string
    Title       string
    PublishedAt *time.Time
}

type ChatResponse struct {
    Text      string
    Parsed    json.RawMessage
    ToolCalls []ToolCall
    Citations []Citation
    Sources   []Source
    Usage     Usage
    Finish    FinishReason
}
```

Points that matter in practice:

- **Send related images in one request.** The model correlates them; separate calls cannot.
- **`Resolution` is your main cost lever.** Medium for everything, high on retry for the
  photo with the unreadable label.
- **Cost is recorded for you.** Never track your own spend; the host attributes every call
  to your plugin and, when inside a job, to that job.
- **Admission reserves budget before dispatch.** The host computes a conservative maximum
  from the request and pricing table, then atomically persists a reservation in integer
  micro-USD. Concurrent calls cannot reserve the same remaining capacity. Completion
  settles actual cost and releases the remainder; a pre-dispatch failure releases all of
  it. A paid call admitted before disable may finish and is still recorded and settled.
- **Two errors you must handle:** `policy.ErrPluginDisabled` and `policy.ErrBudgetExceeded`.
  Both mean stop cleanly, not retry.

---

## 5. Jobs

```go
func (p *Plugin) Jobs() []jobs.Def {
    return []jobs.Def{{
        Name:        "scan",
        Schedule:    "0 3 * * *",      // empty means enqueue-only
        TimeZone:    "UTC",            // required when Schedule is set
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
        if err := jc.Progress(float64(i)/float64(len(lots)), lot.Title); err != nil {
            return err
        }
        if err := jc.Logf("analyzing lot %s", lot.ID); err != nil {
            return err
        }
        ...
    }
    return nil
}
```

**`jobs.Context` is a `context.Context`.** It is cancelled on stop, on timeout, and when
your plugin is disabled. Check it in loops and pass it to every host call. Go cannot kill
a handler or child goroutine that ignores cancellation. The host still rejects new
capability admissions after disable, but an AI call admitted before disable may finish
and will remain in usage accounting.

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

The complete event-facing API is:

```go
type Events interface {
    Publish(ctx context.Context, eventType, subject string, payload any) error
    PublishTx(ctx context.Context, tx storage.Tx, eventType, subject string, payload any) error
}
```

`Publish` inserts the event durably before returning and dispatches only after commit. If
plugin state and its event must be atomic, insert through the transaction-aware overload:

```go
err := h.Store().Tx(ctx, func(tx storage.Tx) error {
    if _, err := tx.Exec(ctx, `UPDATE bidrl_lots SET watched = 1 WHERE id = ?`, lot.ID); err != nil {
        return err
    }
    return h.Events().PublishTx(ctx, tx, "lot.watched", lot.ID, LotWatched{LotID: lot.ID})
})
```

`PublishTx` writes to the event table/outbox in the same transaction. Rollback removes
both changes; commit makes both visible. Never update state and publish in separate
transactions when consumers depend on them being consistent.

**To notify the user, publish an event — do not look for a notify API.** The user writes a
rule against your event type. For the case where you genuinely want to reach them without
any configuration, publish `<plugin>.alert`, which has a default rule:

```go
h.Events().Publish(ctx, "alert", auctionID, Alert{
    Title: "Auction closes in 30 minutes",
    Body:  "6 watched lots still under estimate",
})
```

Each lowercase ASCII segment must match `[a-z][a-z0-9_]*`; the host prefixes the plugin
ID. Use stable descriptive hierarchy such as `lot.analyzed` or `scan.completed`, or a
single segment such as `deal_found`. There is no required noun/verb shape.

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

Every table must start with your plugin ID and an underscore. SQLite-authorizer checks
apply to migration and runtime SQL and reject reads or writes outside that prefix,
cross-namespace triggers/views, attached databases, temporary objects, and unsafe
pragmas. Blob keys are namespaced the same way. This is a mistake guardrail for trusted
code, not a sandbox.

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
| Config | reads one declared setting and observes an update |
| Browser | one `Open` + `Goto` of in-process `hello.test` |
| Log / Clock | writes an attributed log and uses the injected time source |
| UI | one page listing its history |

### Acceptance test

Disabling `hello` while its job is mid-flight must:

1. cancel the running job, recorded as `cancelled` / `plugin_disabled`
2. skip the next cron tick, with no backlog on re-enable
3. reject an `AI().Chat` first attempted after disable with `ErrPluginDisabled`, with no
   provider call or usage row
4. allow an AI call admitted before disable to finish, while recording its usage and
   settling its reservation
5. return `503` from `/api/plugins/hello/*`
6. leave `hello_ticks` readable
7. reject new event publications, SQL/blob mutations, and subscription handler entry;
   leave reads and diagnostic logging available
8. cancel the handler context without claiming to terminate a goroutine that ignores it
9. close admitted browser sessions rather than letting navigation finish

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
