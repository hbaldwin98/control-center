package hosttest

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/hbaldwin98/control-center/host"
	hostevents "github.com/hbaldwin98/control-center/host/events"
	hoststorage "github.com/hbaldwin98/control-center/host/storage"
)

// bus is the event log and the delivery path for one Host.
//
// Delivery is synchronous: a publish returns only once every matching subscription
// has run. The real bus is asynchronous and, for live subscriptions, lossy. That
// difference is deliberate -- a test that has to poll for delivery is a flaky test --
// but it means the double cannot prove a plugin tolerates a dropped live event. Assert
// that with a durable subscription, which the real host does deliver at least once.
type bus struct {
	mu     sync.Mutex
	nextID int64
	log    []hostevents.Event
	subs   []*subscription
	store  *db
	clock  *Clock
	gate   func(mutating bool) error
}

// subscription is one registered handler, live or durable.
type subscription struct {
	pattern string
	name    string // durable name, empty for live
	handler hostevents.Handler
	tx      hostevents.TxHandler
}

func (b *bus) publish(ctx context.Context, tx hoststorage.Tx, pluginID, eventType, subject string, payload any) error {
	if err := b.gate(true); err != nil {
		return err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	e := hostevents.Event{
		Type:      qualify(pluginID, eventType),
		Source:    pluginID,
		Subject:   subject,
		Payload:   raw,
		CreatedAt: b.clock.Now(),
	}
	if tx == nil {
		return b.commit(ctx, e)
	}
	// A transactional publish becomes visible only if the transaction commits, which
	// is the whole reason PublishTx exists.
	tx.AfterCommit(func() { _ = b.commit(ctx, e) })
	return nil
}

// commit assigns the event its ID, appends it to the log, and delivers it.
func (b *bus) commit(ctx context.Context, e hostevents.Event) error {
	b.mu.Lock()
	b.nextID++
	e.ID = b.nextID
	b.log = append(b.log, e)
	subs := append([]*subscription(nil), b.subs...)
	b.mu.Unlock()
	return b.deliver(ctx, e, subs)
}

func (b *bus) deliver(ctx context.Context, e hostevents.Event, subs []*subscription) error {
	for _, s := range subs {
		if !Match(s.pattern, e.Type) {
			continue
		}
		// A disabled plugin receives nothing. The real host does not invoke the
		// handler either; it advances the durable cursor in discard mode.
		if err := b.gate(false); err != nil {
			continue
		}
		if s.handler != nil {
			if err := s.handler(ctx, e); err != nil {
				return err
			}
			continue
		}
		// A durable handler runs inside the cursor transaction: its writes and its
		// acknowledgement commit or roll back together.
		err := b.store.Tx(ctx, func(tx hoststorage.Tx) error {
			return s.tx(ctx, tx, e)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// events returns the log, oldest first.
func (b *bus) events() []hostevents.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]hostevents.Event(nil), b.log...)
}

// register records a plugin's declared subscriptions. The host reads these once, at
// registration, before Migrate or Init.
func (b *bus) register(pluginID string, subs []host.Subscription) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range subs {
		if (s.Handler == nil) == (s.Durable == nil) {
			return errInvalid("subscription %q must set exactly one of Handler or Durable", s.Pattern)
		}
		sub := &subscription{pattern: s.Pattern}
		if s.Durable != nil {
			if s.Durable.Name == "" {
				return errInvalid("durable subscription on %q has no name", s.Pattern)
			}
			if s.Durable.Handler == nil {
				return errInvalid("durable subscription %q has no handler", s.Durable.Name)
			}
			sub.name = "plugin:" + pluginID + ":" + s.Durable.Name
			sub.tx = s.Durable.Handler
		} else {
			sub.handler = s.Handler
		}
		b.subs = append(b.subs, sub)
	}
	return nil
}

// Match reports whether an event type matches a subscription pattern.
//
// Patterns are dot-segmented, with two whole-segment wildcards: "*" matches exactly
// one segment and "**" matches zero or more, anywhere in the pattern. "bidrl.**"
// matches "bidrl.scan.done" and "bidrl"; "bidrl.*" matches neither.
//
// This mirrors the core matcher segment for segment. The conformance suite pins the
// two together, because a subscription that fires in a plugin's own tests and stays
// silent in production is the worst kind of divergence a double can have.
func Match(pattern, eventType string) bool {
	if pattern == "" {
		return true
	}
	return matchSegments(strings.Split(pattern, "."), strings.Split(eventType, "."))
}

func matchSegments(pat, typ []string) bool {
	for len(pat) > 0 {
		switch pat[0] {
		case "**":
			rest := pat[1:]
			if len(rest) == 0 {
				return true
			}
			// "**" matches zero or more segments: try every split.
			for i := 0; i <= len(typ); i++ {
				if matchSegments(rest, typ[i:]) {
					return true
				}
			}
			return false
		case "*":
			if len(typ) == 0 {
				return false
			}
			pat, typ = pat[1:], typ[1:]
		default:
			if len(typ) == 0 || typ[0] != pat[0] {
				return false
			}
			pat, typ = pat[1:], typ[1:]
		}
	}
	return len(typ) == 0
}
