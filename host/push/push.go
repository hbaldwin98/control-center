// Package push is the plugin-facing live-delivery contract.
//
// A plugin never sees a connection, a frame, a heartbeat, or a transport. It names
// topics, publishes payloads to them, and is told when a topic gains its first
// subscriber and loses its last. Everything else -- holding the connection, framing,
// fan-out, backpressure, limits, teardown -- belongs to the host.
//
// That split is deliberate. Live delivery is mechanism, and mechanism written once in
// the host is written once correctly; a plugin that hand-rolls it gets the
// backpressure wrong in its own private way. What travels over the wire is the
// plugin's business and the host does not parse it.
package push

import (
	"context"
	"errors"
)

// Push is the live-delivery capability handed to a plugin.
type Push interface {
	// Publish delivers payload to every connection currently subscribed to topic.
	// It is fire-and-forget: a topic nobody is watching is not an error, and a
	// connection too slow to keep up is dropped rather than allowed to block the
	// publisher. Delivery is therefore best-effort and unordered across topics;
	// anything that must survive a disconnect belongs in the event log or a table.
	Publish(ctx context.Context, topic string, payload any) error

	// Subscribers reports how many connections are watching topic. It is a hint for
	// skipping expensive work nobody would see, never a lock: the count can change
	// the instant it is read.
	Subscribers(topic string) int

	// Watch registers the plugin's reaction to demand. It is called from Init; the
	// returned function stops delivery. Only one watcher is useful per plugin, and a
	// second registration replaces the first.
	Watch(w Watcher) (unwatch func())
}

// Watcher is how a plugin learns that a topic is worth feeding.
//
// Join is called when a topic goes from zero subscribers to one, Leave when it goes
// back to zero. They are serialized per topic, so a Join is always paired with
// exactly one later Leave and the two can never overlap. A Join that returns an error
// is reported to the waiting subscribers as unavailable and no Leave follows it.
//
// Neither call should block: they run on the connection's critical path. Start the
// real work in a goroutine and let Leave cancel it.
type Watcher interface {
	Join(ctx context.Context, topic string) error
	Leave(topic string)
}

// WatcherFuncs adapts two functions to Watcher.
type WatcherFuncs struct {
	OnJoin  func(ctx context.Context, topic string) error
	OnLeave func(topic string)
}

func (w WatcherFuncs) Join(ctx context.Context, topic string) error {
	if w.OnJoin == nil {
		return nil
	}
	return w.OnJoin(ctx, topic)
}

func (w WatcherFuncs) Leave(topic string) {
	if w.OnLeave != nil {
		w.OnLeave(topic)
	}
}

// Errors a plugin can distinguish. Everything else is a host failure the plugin
// cannot act on.
var (
	// ErrInvalidTopic means the name is empty, too long, or uses characters the host
	// cannot carry. Names are otherwise opaque: the host never parses one.
	ErrInvalidTopic = errors.New("push: topic is empty or invalid")
	// ErrLimit means the plugin is at its connection or topic ceiling.
	ErrLimit = errors.New("push: connection or topic limit")
	// ErrPayload means the payload would not encode, or is larger than one message
	// may be. Live delivery carries changes, not documents.
	ErrPayload = errors.New("push: payload is not encodable or too large")
)
