# notifications

**Layer 3** · `internal/core/notifications` · imports `storage`, `events`, `credentials` ·
used by `web` for configuration only

**Responsibility.** Turn committed events into inbox records and external deliveries
through user-configured rules and channels.

No module calls `Send`. Plugins publish domain events; the web module may only administer
rules and channels.

---

## Interface

```go
package notifications

type Rule struct {
    ID       string
    Enabled  bool
    Match    string
    Where    string
    Channels []string
    Title    string
    Body     string
    URL      string
    Throttle time.Duration
}

// Admin is injected only into the authenticated web API. Channel config may contain a
// credential ID but never secret material.
type Admin interface {
    ListRules(ctx context.Context) ([]Rule, error)
    PutRule(ctx context.Context, rule Rule) error
    DeleteRule(ctx context.Context, id string) error
    ListChannels(ctx context.Context) ([]ChannelConfig, error)
    PutChannel(ctx context.Context, channel ChannelConfig) error
    DeleteChannel(ctx context.Context, id string) error
    DeliveryHealth(ctx context.Context) ([]ChannelHealth, error)
}

type Inbox interface {
    List(ctx context.Context, q InboxQuery) (InboxPage, error)
    Get(ctx context.Context, id string) (Notification, error)
    MarkRead(ctx context.Context, id string, read bool) error
}

type InboxQuery struct {
    UnreadOnly bool
    AfterID    string
    Limit      int // 1..200
}

type InboxPage struct {
    Notifications []Notification
    NextAfter     string
}

type ChannelConfig struct {
    ID, Kind, CredentialID string
    Enabled bool
    Settings map[string]string // validated, non-secret settings only
}

type ChannelHealth struct {
    ChannelID, State, LastError string
    LastAttemptAt *time.Time
}

type Channel interface {
    ID() string
    Send(ctx context.Context, d Delivery) error
}

type Delivery struct {
    Notification Notification
    IdempotencyKey string
}

type Notification struct {
    ID            string
    SourceEventID int64
    Title         string
    Body          string
    URL           string
    Subject       string
    Collapsed     int
}
```

Rules and channel configuration are stored and edited through a secret-free admin
surface. A channel that needs a token stores a credential ID and receives a
notifications-scoped `credentials.Runtime`; it does not store or return the token.
`Inbox` is the web-facing source for dashboard alerts, the inbox screen, and mark-read
mutations; it returns no channel credentials or rule internals.
All mutations derive the administrator actor from context, validate patterns,
expressions, templates, channel kind/settings, and referenced credential IDs before
commit, and publish `core.notification.config_changed` transactionally.

---

## Event Consumption

One durable subscriber named `core.notifications` consumes pattern `**`. It does not use
live delivery. On first installation its cursor starts `FromNow` after default rules are
installed; afterward the persisted cursor always resumes. Rule changes affect events
handled after the change and do not replay old events automatically.

Notifications registers with `SubscribeDurableTx`. Its `TxHandler` evaluates all matching
rules and writes inbox, throttle, and external-delivery rows using the transaction supplied
by events. The event cursor advances in that same transaction. A crash therefore leaves
both the inbox work and acknowledgement committed, or neither. Unique constraints make a
retried event safe.

`core.notification.*` events are reserved UI lifecycle events and are never evaluated by
notification rules, preventing notification creation from recursively notifying itself.
An immediately available inbox row publishes `core.notification.ready` in the source
event transaction. When a throttle window closes, the dispatcher publishes that event in
the transaction that makes the digest available. External send state changes publish
`core.notification.delivery_changed`. The web uses these as invalidations and reads the
authoritative rows through REST.

For a rule without throttling, the uniqueness key is `rule ID + source event ID`. The
inbox row persists both this key and `source_event_id`; one send row per channel uses
`uniqueness key + channel ID`. These are structured values in storage, not ambiguous
string concatenation.

---

## Throttling

The throttle subject is derived deterministically:

- If `Event.Subject` is nonempty, use the structured pair `(Event.Source, Event.Subject)`.
- Otherwise use `Event.Type`, so subjectless events of one type can collapse but unrelated
  event types cannot.

The derived value is persisted on the notification and throttle row. The first event
opens a window of the rule's configured duration and creates one delayed inbox/outbox
record. Events for the same rule and throttle subject before `window_ends_at` attach their
source IDs, increment `collapsed_count`, and update the rendered digest instead of
creating another delivery. The uniqueness key is the structured tuple `rule ID + throttle
subject + window start`. The dispatcher may send only after the window closes, so the
inbox and external channels see the same digest.

With `Throttle == 0`, each matching source event creates its own notification.

---

## Delivery

The inbox write is the durable delivery and cannot call an external service. External
channels consume persisted send rows after the notification is ready. Each channel is
at-least-once: a crash after remote acceptance but before recording success may cause a
duplicate. The stable send-row idempotency key is passed to channels that support one;
channels without idempotency support may visibly duplicate.

Transient failures use eight persisted attempts with exponential delays from 1 second,
capped at 5 minutes. Permanent failures stop immediately. Exhausted sends remain failed
and surface in notification health and the settings UI; they do not block the durable
event cursor. A worker claim lease makes abandoned sends retryable after restart.

## Rule Safety

`Where` uses Common Expression Language (CEL) over `event.id`, `event.type`,
`event.source`, `event.subject`, and decoded `event.payload`. No custom functions are
registered, so evaluation has no I/O, reflection, loops, or dynamic code loading. The
host applies fixed input-size, operation-count, and wall-clock limits. A syntax or type
error rejects the rule at save time; a missing optional payload field evaluates to null,
and a runtime failure records one rule error without blocking other rules.

`Title` and `Body` use variable interpolation only. Values are escaped for the target
channel, and inbox rendering treats body text as plain text rather than HTML. `URL` is
either empty or a normalized application-relative path; schemes, protocol-relative URLs,
userinfo, and external hosts are rejected.

---

## Channels In V1

| Channel | Notes |
|---|---|
| `inbox` | In-app and created in the durable event transaction. |
| `webpush` | Browser push; duplicate delivery is possible. |
| `ntfy` | Self-hosted or public topic; uses a stable idempotency key where supported. Creating an enabled ntfy channel attaches it to the `plugin-alert` rule. Deleting a channel removes it from every rule that named it. |

---

## Default Rules

| Pattern | Default channels | Meaning |
|---|---|---|
| `*.alert` | `inbox`, plus each ntfy/webpush channel you add | a plugin's explicit `<plugin>.alert` event |
| `core.job.dead` | `inbox` | a job exhausted its retries |
| `core.plugin.budget_exceeded` | `inbox` | a budget admission was rejected |
| `core.plugin.accounting_invariant_failed` | `inbox` | provider cost exceeded its conservative reservation |
| `core.credential.needs_reauth` | `inbox` | a credential needs a human |
| `core.event.subscription_paused` | `inbox` | a durable subscriber stopped on a poison event |

`*.alert` intentionally matches exactly two-segment plugin alert types. Broader plugin
rules use patterns such as `bidrl.**`; suffix rules use patterns such as `**.failed`.
The default alert rule also requires the source's first segment not to be `core`. Its
title is `{event.subject}` and its body is `{event.payload.body}`, so a plugin that puts
the headline in the subject and details in `payload.body` reaches both the inbox and any
attached ntfy topic without a custom rule. The administrator may add external channels to
any other rule.

---

## Tables

```
core_notification_rules(id, enabled, match, where_expr, channels, title, body, url, throttle_s)
core_notification_channels(id, kind, config, credential_id, enabled)
core_notification_throttles(rule_id, subject_source, subject_value, window_started_at,
                            window_ends_at, notification_id)
core_notifications(id, source_event_id, uniqueness_key, rule_id, subject_source,
                   subject_value, title, body, url, collapsed_count, available_at,
                   created_at, read_at)
core_notification_sources(notification_id, event_id)
core_notification_sends(id, notification_id, channel_id, idempotency_key, state,
                        attempts, next_attempt_at, lease_until, last_error, sent_at)
core_notification_audit(id, actor, object_kind, object_id, action, created_at)
```

Unique constraints cover notification `uniqueness_key`,
`(notification_id, event_id)`, and send `idempotency_key`.
