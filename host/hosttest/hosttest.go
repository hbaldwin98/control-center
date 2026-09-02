// Package hosttest is a test double for the plugin-facing host surface.
//
// It exists so a plugin can be tested from its own module, against the contracts in
// `host`, without importing the control center. A plugin module depends on `host` and
// `host/hosttest` and nothing else.
//
// A double is only worth as much as its fidelity to the real thing. Where the two
// could drift, the rule followed here is to copy core's behaviour exactly and say so
// in a comment at the point of the decision -- event-type prefixing, pattern matching,
// the disable gate, the HTTP mount. Where the double deliberately differs, the comment
// says what a test therefore cannot prove with it. Nothing yet holds the two together
// mechanically; a shared conformance suite that core runs against its real facade is
// the next step, and until it exists these comments are the whole guarantee.
//
// The double is deterministic. Nothing is scheduled in the background: cron does not
// fire, subscriptions deliver when the test publishes, and jobs run when the test says
// to run them. Time only moves when the test moves it.
//
//	h := hosttest.New(t, plugin)
//	h.Run(t)                       // Migrate + Init
//	h.AI.Reply("cheap-chat", "ok") // program the fakes
//	res := h.GET("/status")
package hosttest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hostai "github.com/hbaldwin98/control-center/host/ai"
	hostbrowser "github.com/hbaldwin98/control-center/host/browser"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
	hostpolicy "github.com/hbaldwin98/control-center/host/policy"
	hostpush "github.com/hbaldwin98/control-center/host/push"
	hostsearch "github.com/hbaldwin98/control-center/host/search"
	hoststorage "github.com/hbaldwin98/control-center/host/storage"
)

// DefaultNow is the instant a Host starts at when the test does not choose one. A
// fixed, unremarkable time keeps golden output stable across runs.
var DefaultNow = time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)

// hostImpl is the host.Host handed to plugin code. Tests reach the programmable side
// through Harness, not through this type.
type hostImpl struct {
	id     string
	ai     *AIFake
	brow   *BrowserFake
	search *SearchFake
	push   *PushFake
	jobs   *jobsImpl
	events *eventsImpl
	store  *db
	blobs  *blobs
	config *configImpl
	log    *Logger
	clock  *Clock
}

func (h *hostImpl) PluginID() string             { return h.id }
func (h *hostImpl) AI() hostai.AI                { return h.ai }
func (h *hostImpl) Browser() hostbrowser.Browser { return h.brow }
func (h *hostImpl) Search() hostsearch.Search    { return h.search }
func (h *hostImpl) Jobs() hostjobs.Jobs          { return h.jobs }
func (h *hostImpl) Push() hostpush.Push          { return h.push }
func (h *hostImpl) Events() host.Events          { return h.events }
func (h *hostImpl) Store() hoststorage.DB        { return h.store }
func (h *hostImpl) Blobs() hoststorage.Blobs     { return h.blobs }
func (h *hostImpl) Config() host.Config          { return h.config }
func (h *hostImpl) Log() host.Logger             { return h.log }
func (h *hostImpl) Clock() host.Clock            { return h.clock }

var _ host.Host = (*hostImpl)(nil)

// Clock is the injectable clock plugin code sees. Time stands still until a test
// advances it, so a plugin that schedules work cannot pass by accident.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// Now implements host.Clock.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Set moves the clock to an absolute instant.
func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// Advance moves the clock forward by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

var _ host.Clock = (*Clock)(nil)

// Logger records what the plugin logged and, when a TB is attached, mirrors it into
// the test log so a failing test shows the plugin's own account of what happened.
type Logger struct {
	mu    sync.Mutex
	tb    TB
	attrs []any
	lines *[]LogLine
}

// LogLine is one recorded log call.
type LogLine struct {
	Level string
	Msg   string
	Attrs []any
}

func (l *Logger) Debug(msg string, args ...any) { l.write("debug", msg, args) }
func (l *Logger) Info(msg string, args ...any)  { l.write("info", msg, args) }
func (l *Logger) Warn(msg string, args ...any)  { l.write("warn", msg, args) }
func (l *Logger) Error(msg string, args ...any) { l.write("error", msg, args) }

// With returns a logger carrying additional attributes, like the real host's.
func (l *Logger) With(args ...any) host.Logger {
	l.mu.Lock()
	defer l.mu.Unlock()
	attrs := make([]any, 0, len(l.attrs)+len(args))
	attrs = append(attrs, l.attrs...)
	attrs = append(attrs, args...)
	return &Logger{tb: l.tb, attrs: attrs, lines: l.lines}
}

func (l *Logger) write(level, msg string, args []any) {
	l.mu.Lock()
	attrs := make([]any, 0, len(l.attrs)+len(args))
	attrs = append(attrs, l.attrs...)
	attrs = append(attrs, args...)
	*l.lines = append(*l.lines, LogLine{Level: level, Msg: msg, Attrs: attrs})
	tb := l.tb
	l.mu.Unlock()
	if tb != nil {
		tb.Logf("plugin %s: %s %v", level, msg, attrs)
	}
}

// Lines returns everything logged so far, oldest first.
func (l *Logger) Lines() []LogLine {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]LogLine(nil), *l.lines...)
}

var _ host.Logger = (*Logger)(nil)

// configImpl is the plugin's non-secret settings document. Only the test writes it,
// standing in for the authenticated administration API.
type configImpl struct {
	mu       sync.Mutex
	doc      json.RawMessage
	watchers map[int]func(context.Context, json.RawMessage)
	nextID   int
}

func (c *configImpl) Decode(dst any) error {
	c.mu.Lock()
	doc := c.doc
	c.mu.Unlock()
	if len(doc) == 0 {
		doc = json.RawMessage(`{}`)
	}
	return json.Unmarshal(doc, dst)
}

func (c *configImpl) Watch(fn func(context.Context, json.RawMessage)) func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if fn == nil {
		return func() {}
	}
	id := c.nextID
	c.nextID++
	c.watchers[id] = fn
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.watchers, id)
	}
}

// set replaces the document and notifies watchers synchronously, so a test that
// changes config can assert on the effect on the next line.
func (c *configImpl) set(ctx context.Context, doc json.RawMessage) {
	c.mu.Lock()
	c.doc = doc
	fns := make([]func(context.Context, json.RawMessage), 0, len(c.watchers))
	for _, fn := range c.watchers {
		fns = append(fns, fn)
	}
	c.mu.Unlock()
	for _, fn := range fns {
		fn(ctx, doc)
	}
}

var _ host.Config = (*configImpl)(nil)

// eventsImpl publishes under the plugin's identity. Source is forced to the plugin ID
// and Type is prefixed with it, exactly as the real host does, so a test that asserts
// on an event type sees the name other plugins would subscribe to.
type eventsImpl struct {
	id  string
	bus *bus
}

func (e *eventsImpl) Publish(ctx context.Context, eventType, subject string, payload any) error {
	return e.bus.publish(ctx, nil, e.id, eventType, subject, payload)
}

func (e *eventsImpl) PublishTx(ctx context.Context, tx hoststorage.Tx, eventType, subject string, payload any) error {
	return e.bus.publish(ctx, tx, e.id, eventType, subject, payload)
}

var _ host.Events = (*eventsImpl)(nil)

// qualify is the host's event naming rule: the plugin ID and a dot, always. The real
// scoped bus prefixes unconditionally, so a plugin that publishes "bidrl.scan.done"
// under ID "bidrl" really does produce "bidrl.bidrl.scan.done". The double reproduces
// that rather than being helpful about it -- silently differing here would hide the
// mistake until the event reached production.
func qualify(pluginID, eventType string) string {
	return pluginID + "." + eventType
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// disabledError is what every gated capability returns while a test has the plugin
// disabled, matching the sentinel plugin code is required to handle.
func disabledError() error {
	return fmt.Errorf("hosttest: %w", hostpolicy.ErrPluginDisabled)
}
