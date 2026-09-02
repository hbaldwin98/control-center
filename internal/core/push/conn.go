package push

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// Conn is one live client as the hub and a transport see it. A transport holds it,
// ranges over Messages until the channel closes, and writes what it receives.
type Conn struct {
	svc      *Service
	pluginID string
	topics   []string

	// sendMu guards out against a deliver racing the close that ends the connection.
	sendMu sync.Mutex
	closed bool
	out    chan Message

	once sync.Once
}

// Open admits one connection for a plugin and joins it to its topics. The caller owns
// the returned Conn and must Close it.
func (s *Service) Open(ctx context.Context, pluginID string, topics []string) (*Conn, error) {
	if pluginID == "" {
		return nil, ErrNoPlugin
	}
	if err := s.gate.CheckWork(ctx, pluginID); err != nil {
		return nil, err
	}
	wanted, err := s.normalizeTopics(topics)
	if err != nil {
		return nil, err
	}

	c := &Conn{
		svc:      s,
		pluginID: pluginID,
		topics:   wanted,
		out:      make(chan Message, s.opts.Buffer),
	}

	s.mu.Lock()
	st := s.stateFor(pluginID)
	if len(st.conns) >= s.opts.MaxConnsPerPlugin {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: %d connections", ErrLimit, len(st.conns))
	}
	st.conns[c] = struct{}{}
	for _, name := range wanted {
		t := st.topics[name]
		if t == nil {
			t = &topicState{conns: map[*Conn]struct{}{}}
			st.topics[name] = t
		}
		t.conns[c] = struct{}{}
	}
	s.mu.Unlock()

	// The client is connected before any plugin work runs, so a slow Join shows up as
	// a topic that has not started rather than as a connection that never opened.
	c.deliver(Message{Event: "open", Data: json.RawMessage(fmt.Sprintf(`{"topics":%d}`, len(wanted)))})

	for _, name := range wanted {
		s.reconcile(ctx, pluginID, name)
	}
	return c, nil
}

// normalizeTopics validates, deduplicates, and bounds the requested watch set while
// keeping the caller's order.
func (s *Service) normalizeTopics(topics []string) ([]string, error) {
	if len(topics) == 0 {
		return nil, fmt.Errorf("%w: no topics", ErrInvalidTopic)
	}
	if len(topics) > s.opts.MaxTopicsPerConn {
		return nil, fmt.Errorf("%w: %d topics", ErrLimit, len(topics))
	}
	seen := make(map[string]struct{}, len(topics))
	out := make([]string, 0, len(topics))
	for _, name := range topics {
		if err := ValidTopic(name); err != nil {
			return nil, err
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out, nil
}

// Messages is the stream a transport writes. It closes when the connection ends.
func (c *Conn) Messages() <-chan Message { return c.out }

// PluginID is the plugin this connection belongs to.
func (c *Conn) PluginID() string { return c.pluginID }

// Topics is the resolved watch set, in the order the client asked for it.
func (c *Conn) Topics() []string { return c.topics }

// Close removes the connection and releases any topic it was the last to hold. It is
// safe to call more than once.
func (c *Conn) Close() {
	c.once.Do(func() {
		s := c.svc
		s.mu.Lock()
		st := s.plugins[c.pluginID]
		if st != nil {
			delete(st.conns, c)
			for _, name := range c.topics {
				if t := st.topics[name]; t != nil {
					delete(t.conns, c)
				}
			}
		}
		s.mu.Unlock()

		c.sendMu.Lock()
		c.closed = true
		close(c.out)
		c.sendMu.Unlock()

		// Releasing happens after the connection is gone from the topic, so reconcile
		// sees the count it is meant to converge to.
		for _, name := range c.topics {
			s.reconcile(context.Background(), c.pluginID, name)
		}
	})
}

// deliver queues one message, dropping the connection if it has fallen too far
// behind. A dropped client reconnects and refetches; a blocked publisher would stall
// every other client on the topic.
func (c *Conn) deliver(msg Message) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.closed {
		return
	}
	select {
	case c.out <- msg:
	default:
		c.svc.opts.Log.Warn("push: connection too slow, dropping",
			"plugin", c.pluginID, "topics", len(c.topics))
		go c.Close()
	}
}

// reconcile converges one topic to its demand: joined while at least one connection
// watches it, released once the last leaves. Holding the topic's own lock across the
// read and the plugin call is what makes Join and Leave strictly alternate, however
// the connections that triggered them interleaved.
func (s *Service) reconcile(ctx context.Context, pluginID, topic string) {
	s.mu.Lock()
	st := s.plugins[pluginID]
	if st == nil {
		s.mu.Unlock()
		return
	}
	t := st.topics[topic]
	watcher := st.watcher
	s.mu.Unlock()
	if t == nil {
		return
	}

	t.jmu.Lock()
	defer t.jmu.Unlock()

	s.mu.Lock()
	demand := len(t.conns)
	s.mu.Unlock()

	switch {
	case demand > 0 && !t.joined:
		if watcher == nil {
			// Nothing is feeding this topic. Publishing still works, so this is not
			// an error -- a plugin may drive its topics entirely from elsewhere.
			return
		}
		if err := watcher.Join(ctx, topic); err != nil {
			s.opts.Log.Warn("push: topic join failed", "plugin", pluginID, "topic", topic, "err", err)
			s.notifyUnavailable(pluginID, topic)
			return
		}
		t.joined = true

	case demand == 0 && t.joined:
		t.joined = false
		if watcher != nil {
			watcher.Leave(topic)
		}
		// A topic nobody watches is a map entry nobody reads, and a busy list view
		// would otherwise leave one behind per row it ever showed.
		s.mu.Lock()
		if cur := s.plugins[pluginID]; cur != nil {
			if live := cur.topics[topic]; live == t && len(live.conns) == 0 {
				delete(cur.topics, topic)
			}
		}
		s.mu.Unlock()
	}
}

// notifyUnavailable tells everyone watching a topic that the plugin could not feed it.
func (s *Service) notifyUnavailable(pluginID, topic string) {
	s.mu.Lock()
	var targets []*Conn
	if st := s.plugins[pluginID]; st != nil {
		if t := st.topics[topic]; t != nil {
			targets = make([]*Conn, 0, len(t.conns))
			for c := range t.conns {
				targets = append(targets, c)
			}
		}
	}
	s.mu.Unlock()
	for _, c := range targets {
		c.deliver(Message{Event: "unavailable", Topic: topic, Data: json.RawMessage(`{}`)})
	}
}
