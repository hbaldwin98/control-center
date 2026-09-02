// Package push owns live delivery to connected clients on behalf of plugins.
//
// Layer 3. It imports policy. A plugin names topics and publishes payloads; this
// package owns every connection, its buffer, its limits, and its teardown, and tells
// the plugin only when a topic gains its first subscriber and loses its last.
//
// Transports are adapters over this hub, not implementations of it: sse.go holds an
// HTTP response open and writes what the hub hands it. Adding a second transport adds
// a file here and changes nothing a plugin can see.
package push

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/hbaldwin98/control-center/internal/core/policy"
)

// Errors returned across the push boundary.
var (
	ErrInvalidTopic = errors.New("push: topic is empty or invalid")
	ErrLimit        = errors.New("push: connection or topic limit")
	ErrNoPlugin     = errors.New("push: plugin identity missing")
	ErrPayload      = errors.New("push: payload is not encodable or too large")
	ErrClosed       = errors.New("push: connection closed")
)

const (
	// defaultMaxConnsPerPlugin bounds how many live connections one plugin can hold.
	// A browser opens one per screen showing live data, so this is tabs, not users.
	defaultMaxConnsPerPlugin = 64
	// defaultMaxTopicsPerConn bounds one connection's watch set. A list view watching
	// a page of rows is the shape this has to fit.
	defaultMaxTopicsPerConn = 512
	// defaultBuffer is how far one connection may fall behind before it is dropped.
	// Dropping is the honest failure: a reconnect refetches current state, whereas
	// blocking the publisher would let one slow client stall every other.
	defaultBuffer = 64
	// defaultMaxPayloadBytes bounds one published message.
	defaultMaxPayloadBytes = 64 << 10
	// maxTopicLen bounds a topic name. Names are opaque to the host; only the length
	// and the character set are its business.
	maxTopicLen = 128
)

// Options configures limits. Zero values take the defaults above.
type Options struct {
	Log *slog.Logger

	MaxConnsPerPlugin int
	MaxTopicsPerConn  int
	Buffer            int
	MaxPayloadBytes   int
}

func (o *Options) applyDefaults() {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.MaxConnsPerPlugin <= 0 {
		o.MaxConnsPerPlugin = defaultMaxConnsPerPlugin
	}
	if o.MaxTopicsPerConn <= 0 {
		o.MaxTopicsPerConn = defaultMaxTopicsPerConn
	}
	if o.Buffer <= 0 {
		o.Buffer = defaultBuffer
	}
	if o.MaxPayloadBytes <= 0 {
		o.MaxPayloadBytes = defaultMaxPayloadBytes
	}
}

// Watcher mirrors host/push.Watcher. The core type exists so this package does not
// import the plugin SDK; pluginhost adapts between them.
type Watcher interface {
	Join(ctx context.Context, topic string) error
	Leave(topic string)
}

// Message is one delivery as a transport sees it.
type Message struct {
	// Event is the frame kind: "message" for a published payload, "unavailable" when
	// a plugin could not feed the topic, "open" once when the connection is ready.
	Event string
	Topic string
	Data  json.RawMessage
}

// Service is the concrete unscoped push capability.
type Service struct {
	gate policy.Gate
	opts Options

	mu      sync.Mutex
	plugins map[string]*pluginState

	unwatch func()
}

type pluginState struct {
	watcher Watcher
	conns   map[*Conn]struct{}
	topics  map[string]*topicState
}

type topicState struct {
	// jmu serializes Join and Leave for this topic so a plugin never sees them
	// overlap, and is never held while s.mu is held.
	jmu    sync.Mutex
	joined bool
	conns  map[*Conn]struct{}
}

// New starts watching policy so disabling a plugin drops its connections.
func New(gate policy.Gate, opts Options) (*Service, error) {
	if gate == nil {
		return nil, fmt.Errorf("push: policy gate is required")
	}
	opts.applyDefaults()
	s := &Service{gate: gate, opts: opts, plugins: map[string]*pluginState{}}
	s.unwatch = gate.Watch(func(pluginID string, enabled bool) {
		if !enabled {
			s.ClosePlugin(pluginID)
		}
	})
	return s, nil
}

// Close stops the policy watch and drops every connection.
func (s *Service) Close() {
	if s.unwatch != nil {
		s.unwatch()
		s.unwatch = nil
	}
	s.mu.Lock()
	ids := make([]string, 0, len(s.plugins))
	for id := range s.plugins {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.ClosePlugin(id)
	}
}

// ClosePlugin drops every connection a plugin holds and releases its topics.
func (s *Service) ClosePlugin(pluginID string) {
	s.mu.Lock()
	st := s.plugins[pluginID]
	if st == nil {
		s.mu.Unlock()
		return
	}
	conns := make([]*Conn, 0, len(st.conns))
	for c := range st.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()

	for _, c := range conns {
		c.Close()
	}
}

// stateFor returns the plugin's state, creating it on first use.
func (s *Service) stateFor(pluginID string) *pluginState {
	st := s.plugins[pluginID]
	if st == nil {
		st = &pluginState{conns: map[*Conn]struct{}{}, topics: map[string]*topicState{}}
		s.plugins[pluginID] = st
	}
	return st
}

// SetWatcher registers the plugin's reaction to demand. Passing nil clears it.
func (s *Service) SetWatcher(pluginID string, w Watcher) func() {
	s.mu.Lock()
	st := s.stateFor(pluginID)
	st.watcher = w
	s.mu.Unlock()

	return func() {
		s.mu.Lock()
		if cur := s.plugins[pluginID]; cur != nil && cur.watcher != nil {
			cur.watcher = nil
		}
		s.mu.Unlock()
	}
}

// Publish fans one payload out to the connections watching topic. A topic nobody is
// watching is a no-op, not an error: a plugin should not have to know whether anyone
// is listening in order to be correct.
func (s *Service) Publish(ctx context.Context, pluginID, topic string, payload any) error {
	if strings.TrimSpace(pluginID) == "" {
		return ErrNoPlugin
	}
	if err := ValidTopic(topic); err != nil {
		return err
	}
	if err := s.gate.CheckWork(ctx, pluginID); err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPayload, err)
	}
	if len(body) > s.opts.MaxPayloadBytes {
		return fmt.Errorf("%w: %d bytes", ErrPayload, len(body))
	}

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

	msg := Message{Event: "message", Topic: topic, Data: body}
	for _, c := range targets {
		c.deliver(msg)
	}
	return nil
}

// Subscribers reports how many connections watch a topic right now.
func (s *Service) Subscribers(pluginID, topic string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.plugins[pluginID]
	if st == nil {
		return 0
	}
	t := st.topics[topic]
	if t == nil {
		return 0
	}
	return len(t.conns)
}

// ValidTopic reports whether a topic name is one the host will carry. The host never
// interprets a name; it only refuses ones it cannot address or frame.
func ValidTopic(topic string) error {
	if topic == "" || len(topic) > maxTopicLen {
		return fmt.Errorf("%w: %q", ErrInvalidTopic, topic)
	}
	for _, r := range topic {
		// Printable ASCII minus the comma, which separates names in a transport's
		// topic list, and minus whitespace, which no name needs.
		if r < '!' || r > '~' || r == ',' {
			return fmt.Errorf("%w: %q", ErrInvalidTopic, topic)
		}
	}
	return nil
}
