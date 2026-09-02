package push

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/policy"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

type harness struct {
	t   *testing.T
	ctx context.Context
	svc *Service
	pol *policy.Store
}

func newHarness(t *testing.T, opts Options) *harness {
	t.Helper()
	ctx := context.Background()
	store, err := storage.Open(ctx, storage.Options{Path: filepath.Join(t.TempDir(), "cc.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	bus, err := events.New(store, store, events.Options{PollInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	bus.Start(ctx)
	t.Cleanup(bus.Stop)
	pol, err := policy.New(store, store, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"hello", "other"} {
		if err := pol.Register(ctx, id, false); err != nil {
			t.Fatal(err)
		}
		if err := pol.Enable(ctx, id, "test", "go"); err != nil {
			t.Fatal(err)
		}
	}
	svc, err := New(pol, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return &harness{t: t, ctx: ctx, svc: svc, pol: pol}
}

// recorder is a Watcher that remembers the demand calls it was given.
type recorder struct {
	mu     sync.Mutex
	joins  []string
	leaves []string
	fail   error
	joined chan string
	left   chan string
}

func newRecorder() *recorder {
	return &recorder{joined: make(chan string, 16), left: make(chan string, 16)}
}

func (r *recorder) Join(_ context.Context, topic string) error {
	r.mu.Lock()
	if r.fail != nil {
		err := r.fail
		r.mu.Unlock()
		return err
	}
	r.joins = append(r.joins, topic)
	r.mu.Unlock()
	r.joined <- topic
	return nil
}

func (r *recorder) Leave(topic string) {
	r.mu.Lock()
	r.leaves = append(r.leaves, topic)
	r.mu.Unlock()
	r.left <- topic
}

func (r *recorder) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.joins), len(r.leaves)
}

// next reads one message, failing the test rather than hanging forever.
func next(t *testing.T, c *Conn) Message {
	t.Helper()
	select {
	case msg, open := <-c.Messages():
		if !open {
			t.Fatal("connection closed early")
		}
		return msg
	case <-time.After(5 * time.Second):
		t.Fatal("no message")
	}
	return Message{}
}

// drainOpen consumes the ready frame every connection gets first.
func drainOpen(t *testing.T, c *Conn) {
	t.Helper()
	if msg := next(t, c); msg.Event != "open" {
		t.Fatalf("first frame = %q, want open", msg.Event)
	}
}

func waitFor(t *testing.T, ch chan string, want string) {
	t.Helper()
	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("topic = %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no demand call for %q", want)
	}
}

func TestPublishReachesOnlyTheTopicsSubscribers(t *testing.T) {
	h := newHarness(t, Options{})
	watching, ignoring := mustOpen(t, h, "hello", "lot:1"), mustOpen(t, h, "hello", "lot:2")
	drainOpen(t, watching)
	drainOpen(t, ignoring)

	if err := h.svc.Publish(h.ctx, "hello", "lot:1", map[string]any{"bid": 1500}); err != nil {
		t.Fatal(err)
	}

	msg := next(t, watching)
	if msg.Event != "message" || msg.Topic != "lot:1" {
		t.Fatalf("frame = %+v", msg)
	}
	var got map[string]int
	if err := json.Unmarshal(msg.Data, &got); err != nil || got["bid"] != 1500 {
		t.Fatalf("payload = %s (%v)", msg.Data, err)
	}

	select {
	case stray := <-ignoring.Messages():
		t.Fatalf("unrelated topic received %+v", stray)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestPublishToAnUnwatchedTopicIsNotAnError(t *testing.T) {
	h := newHarness(t, Options{})
	// A plugin should not have to know whether anyone is listening to be correct.
	if err := h.svc.Publish(h.ctx, "hello", "lot:nobody", map[string]any{}); err != nil {
		t.Fatalf("Publish = %v, want nil", err)
	}
}

func TestPluginsDoNotShareTopicNames(t *testing.T) {
	h := newHarness(t, Options{})
	mine := mustOpen(t, h, "hello", "lot:1")
	theirs := mustOpen(t, h, "other", "lot:1")
	drainOpen(t, mine)
	drainOpen(t, theirs)

	if err := h.svc.Publish(h.ctx, "hello", "lot:1", map[string]any{"n": 1}); err != nil {
		t.Fatal(err)
	}
	if msg := next(t, mine); msg.Topic != "lot:1" {
		t.Fatalf("frame = %+v", msg)
	}
	select {
	case stray := <-theirs.Messages():
		t.Fatalf("other plugin received %+v", stray)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestDemandJoinsOnFirstSubscriberAndLeavesOnLast(t *testing.T) {
	h := newHarness(t, Options{})
	rec := newRecorder()
	defer h.svc.SetWatcher("hello", rec)()

	first := mustOpen(t, h, "hello", "lot:1")
	waitFor(t, rec.joined, "lot:1")

	// A second viewer of the same topic is not a second join: that is the whole point
	// of the hub owning fan-out.
	second := mustOpen(t, h, "hello", "lot:1")
	first.Close()
	select {
	case topic := <-rec.left:
		t.Fatalf("left %q while a subscriber remained", topic)
	case <-time.After(200 * time.Millisecond):
	}

	second.Close()
	waitFor(t, rec.left, "lot:1")

	if joins, leaves := rec.counts(); joins != 1 || leaves != 1 {
		t.Fatalf("joins = %d, leaves = %d, want 1 and 1", joins, leaves)
	}
}

func TestDemandRejoinsAfterTheTopicWentQuiet(t *testing.T) {
	h := newHarness(t, Options{})
	rec := newRecorder()
	defer h.svc.SetWatcher("hello", rec)()

	c := mustOpen(t, h, "hello", "lot:1")
	waitFor(t, rec.joined, "lot:1")
	c.Close()
	waitFor(t, rec.left, "lot:1")

	c2 := mustOpen(t, h, "hello", "lot:1")
	defer c2.Close()
	waitFor(t, rec.joined, "lot:1")
	if joins, _ := rec.counts(); joins != 2 {
		t.Fatalf("joins = %d, want a second one", joins)
	}
}

func TestFailedJoinTellsSubscribersTheTopicIsUnavailable(t *testing.T) {
	h := newHarness(t, Options{})
	rec := newRecorder()
	rec.fail = errors.New("upstream refused")
	defer h.svc.SetWatcher("hello", rec)()

	c := mustOpen(t, h, "hello", "lot:1")
	drainOpen(t, c)

	msg := next(t, c)
	if msg.Event != "unavailable" || msg.Topic != "lot:1" {
		t.Fatalf("frame = %+v, want unavailable for lot:1", msg)
	}
	// A failed join is not a join, so no Leave follows it.
	if _, leaves := rec.counts(); leaves != 0 {
		t.Fatalf("leaves = %d, want none after a failed join", leaves)
	}
}

func TestSlowConnectionIsDroppedRatherThanBlockingThePublisher(t *testing.T) {
	h := newHarness(t, Options{Buffer: 2})
	slow := mustOpen(t, h, "hello", "lot:1")
	// Never drained: the open frame already occupies part of the buffer.
	for i := 0; i < 20; i++ {
		if err := h.svc.Publish(h.ctx, "hello", "lot:1", map[string]any{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, open := <-slow.Messages():
			if !open {
				return // dropped, which is the contract
			}
		case <-deadline:
			t.Fatal("slow connection was never dropped")
		}
	}
}

func TestDisablingThePluginEndsItsConnections(t *testing.T) {
	h := newHarness(t, Options{})
	rec := newRecorder()
	defer h.svc.SetWatcher("hello", rec)()

	c := mustOpen(t, h, "hello", "lot:1")
	waitFor(t, rec.joined, "lot:1")

	if err := h.pol.Disable(h.ctx, "hello", "test", "kill switch"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, open := <-c.Messages():
			if !open {
				// The upstream work the topic represented must be released too, or a
				// disabled plugin would keep feeding a socket nobody reads.
				waitFor(t, rec.left, "lot:1")
				return
			}
		case <-deadline:
			t.Fatal("disable did not close the connection")
		}
	}
}

func TestDisabledPluginCannotOpenOrPublish(t *testing.T) {
	h := newHarness(t, Options{})
	if err := h.pol.Disable(h.ctx, "hello", "test", "kill switch"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Open(h.ctx, "hello", []string{"lot:1"}); !errors.Is(err, policy.ErrPluginDisabled) {
		t.Fatalf("Open error = %v, want ErrPluginDisabled", err)
	}
	if err := h.svc.Publish(h.ctx, "hello", "lot:1", map[string]any{}); !errors.Is(err, policy.ErrPluginDisabled) {
		t.Fatalf("Publish error = %v, want ErrPluginDisabled", err)
	}
}

func TestOpenRejectsUnusableWatchSets(t *testing.T) {
	h := newHarness(t, Options{MaxTopicsPerConn: 2})
	cases := map[string][]string{
		"no topics":     {},
		"empty name":    {""},
		"has a comma":   {"lot:1,lot:2"},
		"has a space":   {"lot 1"},
		"too many":      {"a", "b", "c"},
		"non printable": {"lot:\x01"},
	}
	for name, topics := range cases {
		if _, err := h.svc.Open(h.ctx, "hello", topics); err == nil {
			t.Errorf("Open(%s) succeeded, want rejection", name)
		}
	}
	if _, err := h.svc.Open(h.ctx, "", []string{"lot:1"}); !errors.Is(err, ErrNoPlugin) {
		t.Errorf("Open without a plugin = %v, want ErrNoPlugin", err)
	}
}

func TestOpenEnforcesTheConnectionLimit(t *testing.T) {
	h := newHarness(t, Options{MaxConnsPerPlugin: 2})
	for i := 0; i < 2; i++ {
		if _, err := h.svc.Open(h.ctx, "hello", []string{"lot:1"}); err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
	}
	if _, err := h.svc.Open(h.ctx, "hello", []string{"lot:1"}); !errors.Is(err, ErrLimit) {
		t.Fatalf("error = %v, want ErrLimit", err)
	}
}

func TestPublishRejectsAnOversizedPayload(t *testing.T) {
	h := newHarness(t, Options{MaxPayloadBytes: 64})
	big := make([]byte, 128)
	for i := range big {
		big[i] = 'x'
	}
	if err := h.svc.Publish(h.ctx, "hello", "lot:1", map[string]any{"blob": string(big)}); !errors.Is(err, ErrPayload) {
		t.Fatalf("error = %v, want ErrPayload", err)
	}
}

func TestSubscribersCountsCurrentDemand(t *testing.T) {
	h := newHarness(t, Options{})
	if n := h.svc.Subscribers("hello", "lot:1"); n != 0 {
		t.Fatalf("Subscribers = %d, want 0", n)
	}
	c := mustOpen(t, h, "hello", "lot:1")
	if n := h.svc.Subscribers("hello", "lot:1"); n != 1 {
		t.Fatalf("Subscribers = %d, want 1", n)
	}
	c.Close()
	if n := h.svc.Subscribers("hello", "lot:1"); n != 0 {
		t.Fatalf("Subscribers after close = %d, want 0", n)
	}
}

func TestDuplicateTopicsCollapseToOneSubscription(t *testing.T) {
	h := newHarness(t, Options{})
	c := mustOpen(t, h, "hello", "lot:1", "lot:1", "lot:2")
	if got := c.Topics(); len(got) != 2 {
		t.Fatalf("topics = %v, want two", got)
	}
	drainOpen(t, c)
	if err := h.svc.Publish(h.ctx, "hello", "lot:1", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	next(t, c)
	select {
	case dup := <-c.Messages():
		t.Fatalf("delivered twice: %+v", dup)
	case <-time.After(100 * time.Millisecond):
	}
}

func mustOpen(t *testing.T, h *harness, pluginID string, topics ...string) *Conn {
	t.Helper()
	c, err := h.svc.Open(h.ctx, pluginID, topics)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}
