# events

**Layer 1** · `internal/core/events` · imports `storage` · used by every module above

**Responsibility.** Persist every event, then dispatch committed events to live and
durable subscribers.

This is the spine. Modules do not call each other sideways; they publish here.

---

## Interface

```go
package events

type Event struct {
    ID        int64           // monotonic within the persisted log
    Type      string          // "bidrl.deal_found", "core.job.failed"
    Source    string          // plugin ID, or "core.<module>"
    Subject   string          // optional stable entity ID: lot ID, job ID
    Payload   json.RawMessage
    CreatedAt time.Time
}

type Input struct {
    Type    string
    Source  string
    Subject string
    Payload any
}

type Query struct {
    Pattern string
    AfterID int64
    Limit   int // 1..1000
}

type Bus interface {
    // Publish commits one event in its own transaction.
    Publish(ctx context.Context, in Input) (int64, error)

    // PublishTx inserts into tx. A rollback removes the event. Dispatchers tail the
    // committed log, so they cannot observe it before the publisher commits.
    PublishTx(ctx context.Context, tx storage.Tx, in Input) (int64, error)

    SubscribeLive(pattern string, h Handler) (Subscription, error)
    SubscribeDurable(cfg DurableConfig, h Handler) (Subscription, error)
    SubscribeDurableTx(cfg DurableConfig, h TxHandler) (Subscription, error)
    Query(ctx context.Context, q Query) ([]Event, error)
}

type Handler func(ctx context.Context, e Event) error

// The dispatcher invokes a durable handler and advances its cursor in the same storage
// transaction. Returning an error rolls back both the handler's writes and the cursor.
type TxHandler func(ctx context.Context, tx storage.Tx, e Event) error

type DurableConfig struct {
    Name    string       // globally unique stable subscriber name
    Pattern string
    Start   CursorStart  // used only when no persisted cursor exists
    Retry   RetryPolicy
}

type CursorStart struct {
    Mode    StartMode    // FromNow, FromBeginning, or AfterEvent
    EventID int64        // required only for AfterEvent
}

type StartMode string
const (
    FromNow       StartMode = "now"
    FromBeginning StartMode = "beginning"
    AfterEvent    StartMode = "after_event"
)

type RetryPolicy struct {
    MaxAttempts int
    Initial     time.Duration
    Maximum     time.Duration
}
```

For a new durable name, `FromNow` atomically stores the current log tail,
`FromBeginning` stores zero, and `AfterEvent` stores the supplied event ID. If a cursor
already exists, it always resumes and ignores `Start`. Reusing a name with a different
pattern is an error; resetting a cursor is an explicit administrative operation.

---

## Delivery

Live subscriptions receive committed events through bounded in-memory queues. They are
lossy: process restarts, handler failures, and slow consumers may drop events, and there
is no retry or replay. Use them only for explicitly ephemeral reactions. The web SSE
endpoint does not use this primitive; it tails the persisted log so queue overflow cannot
silently create a client gap.

A durable subscription reads only the persisted log, in increasing event ID order. One
handler invocation may run at a time for each durable name. Nonmatching rows advance the
cursor immediately; a matching row advances it only after the handler succeeds. This
provides at-least-once handling: a crash after an external side effect but before cursor
commit can repeat that effect.

`SubscribeDurableTx` opens the cursor transaction before invoking `TxHandler` and commits
the handler's database writes with the cursor advance. `SubscribeDurable` invokes an
ordinary handler without holding a write transaction, then advances its cursor in a
short transaction. Use the transactional form when handler state and acknowledgement
must be crash-consistent, as notifications does.

Replay and new events use the same `id > last_event_id` query, so there is no separate
replay/live handoff and no race between them. A database-backed lease permits only one
active dispatcher for a durable name, including across processes.

The default durable retry policy is 10 attempts with exponential delays from 1 second,
capped at 5 minutes. Attempt count and next retry time are persisted. After the tenth
failure, the subscription pauses on that poison event, reports unhealthy status, and
publishes `core.event.subscription_paused` for operational alerting. An operator must
retry, skip, or reset it; later events do not pass it silently.

`MaxAttempts` must be positive and backoffs must be finite and positive. A configured
policy overrides the defaults and its `MaxAttempts` is the poison-event threshold.

---

## Naming And Patterns

An event type is a source namespace followed by one or more dot-separated segments. Each
segment uses the ASCII grammar `[a-z][a-z0-9_]*`, so a complete type matches:

```
^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$
```

The first segment is `core` for core modules or the plugin ID for plugin events. The
remaining segments describe the event; existing names are valid and no universal
noun/past-tense shape is required.

`Input` is validated before insertion. Core publishers must use their assigned
`core.<module>` source; scoped plugin publishers cannot set `Source` and receive their
validated plugin ID plus event-type prefix from the facade. Serialized payloads are at
most 256 KiB. Invalid types, source mismatches, and oversized payloads are rejected.
The scoped plugin facade also checks policy in the publication transaction. A publication
therefore commits before a concurrent disable or is rejected after it; plugin reads and
core-module publication do not use this plugin gate.

Patterns use the same literal segments plus two whole-segment wildcards: `*` matches
exactly one segment, while `**` matches zero or more. For example, `bidrl.**` matches all
BidRL events, `core.job.*` matches one event segment below `core.job`, and `**.failed`
matches every type whose final segment is `failed`. `**` matches everything.

---

## Core Event Catalogue

| Event | Source | Payload |
|---|---|---|
| `core.job.enqueued` | `core.jobs` | job ID, plugin, name, args digest |
| `core.job.started` | `core.jobs` | job ID, plugin, name, attempt, fence generation |
| `core.job.progressed` | `core.jobs` | job ID, plugin, name, fraction, message |
| `core.job.logged` | `core.jobs` | job ID, plugin, name, through log ID |
| `core.job.succeeded` | `core.jobs` | job ID, plugin, name, attempt, duration |
| `core.job.failed` | `core.jobs` | job ID, plugin, name, attempt, error class |
| `core.job.cancelled` | `core.jobs` | job ID, plugin, name, attempt, reason |
| `core.job.dead` | `core.jobs` | job ID, plugin, name, attempts, last error |
| `core.ai.usage` | `core.ai` | plugin, job, operation, logical model, status, usage totals, attempts, cost in micro-USD |
| `core.plugin.enabled` / `.disabled` | `core.policy` | plugin, actor, reason |
| `core.plugin.budget_exceeded` | `core.policy` | plugin, window, limit, reserved, committed, action |
| `core.plugin.accounting_invariant_failed` | `core.policy` | plugin, reservation ID, reserved maximum, actual cost |
| `core.credential.needs_reauth` | `core.credentials` | provider, credential ID |
| `core.event.subscription_paused` | `core.events` | subscriber, event ID, attempts, last error |
| `core.notification.ready` / `.delivery_changed` | `core.notifications` | notification ID, state, channel when applicable |
| `core.notification.config_changed` | `core.notifications` | actor, object kind, object ID, action |

State changes and their required events use `PublishTx`; publishing after a state commit
is not acceptable.

---

## Tables

```
core_events(id, type, source, subject, payload, created_at)
core_event_cursors(subscriber, pattern, last_event_id, state, failed_event_id,
                   attempts, retry_at, lease_owner, lease_until, updated_at)
```

Indexes cover `(type, id)` and `(source, id)`. V1 retains events for at least 90 days and
also retains every row still needed by an active or paused durable cursor. Removing,
skipping, or resetting a durable subscriber explicitly releases that pin. A daily
retention job deletes only rows older than both boundaries and reports disk pressure when
a stalled cursor prevents cleanup. The oldest retained ID is exposed to SSE so a client
behind that boundary receives a reset rather than a partial replay.
