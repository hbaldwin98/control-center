package hosttest

import (
	"context"
	"encoding/json"
	"sort"
	"sync"

	hostpush "github.com/hbaldwin98/control-center/host/push"
)

// PushFake is live delivery without a transport.
//
// The real host holds the connections and decides when a topic gains its first
// subscriber and loses its last. Here the test plays that part: Subscribe and
// Unsubscribe stand in for a screen opening and closing, and they call the plugin's
// watcher exactly where the host would. Everything the plugin publishes is recorded
// rather than framed and written to a socket.
//
// Delivery is synchronous, so a payload published during Subscribe has already been
// recorded when Subscribe returns.
type PushFake struct {
	mu       sync.Mutex
	watcher  hostpush.Watcher
	subs     map[string]int
	messages []PushMessage
	state    map[string]string // topic -> "available" | "unavailable"
	gate     func(mutating bool) error
}

// PushMessage is one thing the plugin sent to a topic.
type PushMessage struct {
	Topic string
	// Payload is the encoded form, because that is what a subscriber would receive.
	Payload json.RawMessage
}

func (p *PushFake) Publish(ctx context.Context, topic string, payload any) error {
	if err := p.gate(true); err != nil {
		return err
	}
	if topic == "" {
		return hostpush.ErrInvalidTopic
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return hostpush.ErrPayload
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	// A topic nobody is watching is not an error: the plugin is allowed to publish
	// into the void, and the real host drops it.
	p.messages = append(p.messages, PushMessage{Topic: topic, Payload: raw})
	return nil
}

func (p *PushFake) Available(ctx context.Context, topic string) error {
	return p.setState(topic, "available")
}

func (p *PushFake) Unavailable(ctx context.Context, topic string) error {
	return p.setState(topic, "unavailable")
}

func (p *PushFake) setState(topic, state string) error {
	if err := p.gate(true); err != nil {
		return err
	}
	if topic == "" {
		return hostpush.ErrInvalidTopic
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state[topic] = state
	return nil
}

func (p *PushFake) Subscribers(topic string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.subs[topic]
}

func (p *PushFake) Watch(w hostpush.Watcher) func() {
	p.mu.Lock()
	defer p.mu.Unlock()
	// A second registration replaces the first, as the real host documents.
	p.watcher = w
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.watcher = nil
	}
}

var _ hostpush.Push = (*PushFake)(nil)

// Subscribe stands in for a client watching a topic. The first subscriber calls the
// plugin's Join, exactly as a real connection would; later ones only raise the count.
// It returns the error Join gave, because that is what a real subscriber would be
// told through an unavailable notice.
func (p *PushFake) Subscribe(ctx context.Context, topic string) error {
	p.mu.Lock()
	p.subs[topic]++
	first := p.subs[topic] == 1
	w := p.watcher
	p.mu.Unlock()

	if !first || w == nil {
		return nil
	}
	if err := w.Join(ctx, topic); err != nil {
		// A failed Join gets no Leave, and the topic is reported as not being fed.
		p.mu.Lock()
		p.subs[topic]--
		p.state[topic] = "unavailable"
		p.mu.Unlock()
		return err
	}
	return nil
}

// Unsubscribe drops one watcher. The last one out calls the plugin's Leave.
func (p *PushFake) Unsubscribe(topic string) {
	p.mu.Lock()
	if p.subs[topic] > 0 {
		p.subs[topic]--
	}
	last := p.subs[topic] == 0
	w := p.watcher
	if last {
		delete(p.subs, topic)
	}
	p.mu.Unlock()

	if last && w != nil {
		w.Leave(topic)
	}
}

// Messages returns everything published, oldest first.
func (p *PushFake) Messages() []PushMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]PushMessage(nil), p.messages...)
}

// MessagesOn returns what was published to one topic, oldest first.
func (p *PushFake) MessagesOn(topic string) []PushMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []PushMessage
	for _, m := range p.messages {
		if m.Topic == topic {
			out = append(out, m)
		}
	}
	return out
}

// State reports what the plugin last said about a topic: "available", "unavailable",
// or "" if it never said anything. It is how a test checks that a screen would be
// told the values it is showing have stopped being fed.
func (p *PushFake) State(topic string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state[topic]
}

// Topics lists every topic published to or marked, sorted, for assertions that care
// about which topics a plugin decided to feed.
func (p *PushFake) Topics() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	seen := map[string]struct{}{}
	for _, m := range p.messages {
		seen[m.Topic] = struct{}{}
	}
	for topic := range p.state {
		seen[topic] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for topic := range seen {
		out = append(out, topic)
	}
	sort.Strings(out)
	return out
}
