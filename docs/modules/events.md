# events

**Layer 1** · `internal/core/events` · imports `storage` · used by every module above

**Responsibility.** Persist every event, then dispatch it to live and durable subscribers.

This is the spine. Modules do not call each other sideways; they publish here.

---

## Interface

```go
package events

type Event struct {
    ID        int64           // monotonic
    Type      string          // "bidrl.deal_found", "core.job.failed"
    Source    string          // plugin ID, or "core.<module>"
    Subject   string          // optional entity ID: lot ID, job ID
    Payload   json.RawMessage
    CreatedAt time.Time
}

type Bus interface {
    // Publish persists the event, then dispatches. Returns after persistence,
    // not after dispatch, so a slow handler never blocks the publisher.
    Publish(ctx context.Context, in Input) (int64, error)

    Subscribe(pattern string, h Handler, opts ...SubOpt) (Subscription, error)

    Query(ctx context.Context, q Query) ([]Event, error)
}

type Handler func(ctx context.Context, e Event) error

// Durable makes a subscription cursor-tracked: it records the last event ID it
// handled and replays from there on startup. Without it, a subscription is live
// only — in-process, best effort, failures logged rather than retried.
func Durable(name string) SubOpt
```

Choose live for UI streams and notification rules. Choose durable where missing an event
is a correctness problem.

---

## Naming

**`<source>.<noun>.<past-tense-verb>`** — `bidrl.lot.analyzed`, `core.job.failed`,
`core.ai.usage`.

Consistency here is what makes rules writable later, so it is a hard convention rather
than a suggestion.

**Patterns** are dot-segment globs:

| Pattern | Matches |
|---|---|
| `bidrl.*` | one further segment under `bidrl` |
| `core.job.*` | `core.job.failed`, `core.job.dead` |
| `*.failed` | any source, one segment, ending `failed` |
| `**` | everything |

---

## Core event catalogue

| Event | Source | Payload |
|---|---|---|
| `core.job.enqueued` / `.started` / `.succeeded` / `.failed` / `.cancelled` / `.dead` | `core.jobs` | job ID, plugin, name, attempt, error |
| `core.ai.usage` | `core.ai` | plugin, job, logical + provider model, tokens, cost |
| `core.plugin.enabled` / `.disabled` | `core.policy` | plugin, actor, reason |
| `core.plugin.budget_exceeded` | `core.policy` | plugin, window, limit, spent, action |
| `core.credential.needs_reauth` | `core.credentials` | provider, credential ID |

---

## Tables

```
core_events(id, type, source, subject, payload, created_at)
core_event_cursors(subscriber, last_event_id, updated_at)
```

Index on `(type, id)` and `(source, id)`.

---

## Notes

- **Persist before dispatch.** A handler panic must never lose the event.
- **Publishers do not learn about subscribers.** No return value says whether anything
  handled it.
- Retention is an [open question](../../DESIGN.md#9-open-questions); `core_events` grows
  without bound today.
