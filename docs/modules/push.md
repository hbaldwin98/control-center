# push

**Layer 3** · `internal/core/push` · imports `policy` · used by `pluginhost`, `web`

**Responsibility.** Own live delivery to connected clients: connections, topics,
fan-out, backpressure, limits, and teardown. A plugin names topics and publishes
payloads; it writes no transport code.

---

## Interface

```go
package push

// What a plugin gets, through host/push.
type Push interface {
    Publish(ctx context.Context, topic string, payload any) error
    Subscribers(topic string) int
    Watch(w Watcher) (unwatch func())
}

// How a plugin learns a topic is worth feeding.
type Watcher interface {
    Join(ctx context.Context, topic string) error
    Leave(topic string)
}
```

`Join` fires when a topic goes from zero subscribers to one, `Leave` when it goes back
to zero. They are serialized per topic, so a `Join` is always paired with exactly one
later `Leave` and the two never overlap. A `Join` that returns an error is reported to
the waiting subscribers as `unavailable`, and no `Leave` follows it. Neither call may
block: they run on the connection's critical path, so start real work in a goroutine
and let `Leave` cancel it.

---

## Why this is a host concern

Live delivery is mechanism. Holding a connection, framing it, heartbeating it, bounding
how far one client may fall behind, and tearing it all down on disable is the same work
for every plugin, and it is the kind of work that is wrong in a subtly different way
each time it is written by hand. So the host owns it exactly once.

What travels is not mechanism, and the host does not parse it. A topic is an opaque
name the plugin defines; a payload is whatever the plugin marshals. The boundary is
deliberately drawn there: **rules, not limits.**

The refcount is what makes the hub worth having rather than just tidy. Ten screens
watching the same row is one `Join`, so the plugin does one lot of upstream work rather
than ten, and a topic nobody is watching costs nothing. A per-request stream can never
do that, because each request would have to do its own work and could not see the
others.

---

## Topics

A topic is a non-empty string of at most 128 printable ASCII characters, excluding
whitespace and the comma (which separates names in a transport's topic list). Nothing
else about a name is the host's business — `lot:1001` and `check:7:status` are equally
opaque to it.

Topics are namespaced per plugin. Two plugins naming the same topic never see each
other's subscribers or payloads.

---

## Delivery

`Publish` is fire-and-forget:

- A topic nobody is watching is a no-op, not an error. A plugin should not have to know
  whether anyone is listening in order to be correct.
- A connection too slow to drain is **dropped**, not waited for. Blocking the publisher
  would let one stalled client stall every other client on the topic. A dropped client
  reconnects and refetches.

So delivery is best-effort and unordered across topics. Anything that must survive a
disconnect belongs in the event log or a table. The pattern to follow is BidRL's: write
the change, then publish it. The database stays the one source of truth, and the
message only saves a screen from refetching a list to learn one number.

---

## Limits

| Limit | Default | Why |
|---|---|---|
| Connections per plugin | 64 | A browser opens one per screen showing live data; this is tabs, not users. |
| Topics per connection | 512 | A list view watching a page of rows is the shape this has to fit. |
| Buffer per connection | 64 messages | How far one client may fall behind before it is dropped. |
| Payload per message | 64 KiB | Live delivery carries changes, not documents. |

Disabling a plugin drops its connections and releases every topic it held, so a killed
plugin cannot keep feeding a socket nobody reads.

---

## Transports

Transports are adapters over the hub, not implementations of it. The hub hands a
transport a `*Conn` and a channel of messages; the transport writes them.

**SSE** (`sse.go`) is the one that exists. The core mounts it at
`GET /api/push/{plugin}?topics=a,b`, behind the same authentication as every other API
route, and `topics` accepts both a repeated parameter and one comma-separated value.
Frames look like:

```
event: ready
data: {"topic":"","data":{"topics":2}}

event: message
data: {"topic":"lot:1001","data":{"lotId":"1001","currentBidCents":1800}}

event: unavailable
data: {"topic":"lot:1001","data":{}}

event: closed
data: {"topic":"","data":{}}
```

The topic rides in the body rather than the event name, so a client registers one
listener per kind instead of one per row on screen. The ready frame is not called
`open`, because `EventSource` defines an `open` event of its own and a listener could
not tell the server's frame from the browser's.

**A client must not close the stream on an error.** `EventSource` reconnects by itself,
and calling `close()` in an error handler turns one transient drop into a permanent
one — the connection delivers a message or two and then is gone for good. Read
`readyState` instead: `2` means the browser has given up, anything else means it is
retrying.

A **websocket** transport is another file this size, and the right time to add it is
when a client needs to *send* as well as receive — change its watched set mid-stream,
acknowledge, carry a cursor. Until then SSE is the cheaper answer: no upgrade, and
`EventSource` reconnects on its own. Because the plugin contract is topics and payloads
rather than frames, adding it changes nothing a plugin can observe.

What to avoid is offering both for the same feature as a client preference. Two live
paths to the same data means two sets of reconnect semantics and two places for a
drop-policy bug, only one of which the tests really exercise. Let the transport follow
from what the client has to do.

---

## What this is not

It is not the core event stream. `/api/stream` tails the durable event log, replays
from a resume point, and is how a screen learns that something it is showing is stale.
Push carries no history and guarantees no delivery; it is for a value a screen can fold
in immediately. A plugin that needs both uses both, as BidRL does: a catalog refresh
across a hundred lots is an event, and one bid on one lot is a message.

It is not a job queue. A connection lasts as long as someone is looking at a screen and
leaves nothing behind worth resuming. See `docs/modules/jobs.md` for the other side of
that line.
