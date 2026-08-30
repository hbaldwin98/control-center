# notifications

**Layer 3** · `internal/core/notifications` · imports `storage`, `events` ·
used by `web` for configuration only

**Responsibility.** Turn events into deliveries, via user-configured rules and channels.

---

## The invariant

**This module has no inbound callers.** Nothing in the system calls "send me a
notification." It subscribes to the bus and calls out to channels.

That is what keeps the dependency direction clean. If `jobs` could call
`notifications.Send`, every module would eventually import every other one and the layer
rule would be fiction.

**How a plugin notifies:** it does not. It publishes a domain event, and the user writes a
rule.

---

## Interface

```go
package notifications

type Rule struct {
    ID       string
    Enabled  bool
    Match    string        // event pattern, e.g. "bidrl.deal_found"
    Where    string        // optional expression over the payload
    Channels []string      // channel IDs
    Title    string        // template over the event
    Body     string
    Throttle time.Duration // collapses repeats of the same rule + subject
}

type Channel interface {
    ID() string
    Send(ctx context.Context, n Notification) error
}

type Notification struct {
    Title   string
    Body    string
    URL     string // deep link into the UI
    Subject string
}
```

Rules are stored, not compiled in, and are edited in the settings UI.

---

## Channels in v1

| Channel | Notes |
|---|---|
| `inbox` | In-app. Always enabled; the fallback that cannot fail. |
| `webpush` | Browser push, for the phone. |
| `ntfy` | Self-hosted or public ntfy topic. |

Each is a small adapter behind `Channel`. Adding one should not touch anything else.

---

## Default rules

A new plugin should be useful before the user configures anything, so the host ships
defaults for conventional types:

| Pattern | Delivers |
|---|---|
| `*.alert` | any plugin's explicit "tell me" event |
| `core.job.dead` | a job exhausted its retries |
| `core.plugin.budget_exceeded` | a budget window was crossed |
| `core.credential.needs_reauth` | a credential needs a human |

`*.alert` is the escape valve: a plugin that genuinely wants to reach the user emits
`<plugin>.alert` and the default rule delivers it — without the plugin importing this
module or knowing that ntfy exists.

---

## Tables

```
core_notification_rules(id, enabled, match, where_expr, channels, title, body, throttle_s)
core_notifications(id, rule_id, subject, title, body, url, created_at, read_at)
core_notification_sends(id, notification_id, channel, status, error, sent_at)
```

---

## Notes

- **Throttling is per rule + subject.** Fifty `bidrl.deal_found` events in one scan should
  produce one digest, not fifty pushes.
- Delivery failure is logged and surfaced in the UI; it never retries forever and never
  blocks the bus.
