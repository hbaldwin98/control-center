package browser

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// useFeedServer points the subscribe client at a local websocket server. The URL a
// plugin passes still has to survive the wss allowlist check; only the dial is
// redirected, the way useHTTPServer redirects an ordinary fetch.
func (h *harness) useFeedServer(server *httptest.Server) {
	h.t.Helper()
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		h.t.Fatal(err)
	}
	base := server.Client()
	transport := base.Transport
	base.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = serverURL.Scheme
		clone.URL.Host = serverURL.Host
		return transport.RoundTrip(clone)
	})
	h.svc.opts.SubscribeClient = base
}

func feedServer(t *testing.T, handle func(ctx context.Context, c *websocket.Conn)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		handle(r.Context(), c)
	}))
	t.Cleanup(server.Close)
	return server
}

func openFeedSession(t *testing.T, h *harness) Session {
	t.Helper()
	sess, err := h.svc.Open(h.ctxHello(), OpenOptions{AllowedHosts: []string{"feed.test"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close(h.ctxHello()) })
	return sess
}

func TestSubscribeStreamsFramesAndAnswersDeclaredHeartbeat(t *testing.T) {
	pongs := make(chan string, 1)
	server := feedServer(t, func(ctx context.Context, c *websocket.Conn) {
		// The handshake the plugin declared arrives first.
		_, sub, err := c.Read(ctx)
		if err != nil {
			return
		}
		if !strings.Contains(string(sub), "new_high_bid-42") {
			t.Errorf("handshake = %s", sub)
		}
		if err := c.Write(ctx, websocket.MessageText, []byte(`{"event":"heartbeat"}`)); err != nil {
			return
		}
		_, reply, err := c.Read(ctx)
		if err != nil {
			return
		}
		pongs <- string(reply)
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"event":"new_high_bid","data":"{\"item_id\":1001}"}`))
		<-ctx.Done()
	})

	h := newHarness(t, map[string]http.Handler{"unused.test": http.NotFoundHandler()})
	h.useFeedServer(server)
	sess := openFeedSession(t, h)

	sub, err := sess.Subscribe(h.ctxHello(), "wss://feed.test/app/key", SubscribeOptions{
		Handshake: [][]byte{[]byte(`{"event":"subscribe","data":{"channel":"new_high_bid-42"}}`)},
		KeepAlive: []KeepAliveRule{{Event: "heartbeat", Reply: []byte(`{"event":"pong"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close(h.ctxHello())

	select {
	case got := <-pongs:
		if !strings.Contains(got, `"pong"`) {
			t.Fatalf("heartbeat reply = %s", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no heartbeat reply")
	}

	// Answering a heartbeat does not consume it: the plugin still sees every frame the
	// server sent, in order.
	deadline := time.After(5 * time.Second)
	var events []string
	for len(events) < 2 {
		select {
		case frame := <-sub.Frames():
			if frame.At.IsZero() {
				t.Error("frame has no receive time")
			}
			events = append(events, string(frame.Data))
		case <-deadline:
			t.Fatalf("frames = %q, want the heartbeat and the bid", events)
		}
	}
	if !strings.Contains(events[0], "heartbeat") || !strings.Contains(events[1], "new_high_bid") {
		t.Fatalf("frames = %q", events)
	}
}

func TestSubscribeChecksSchemeAndAllowlist(t *testing.T) {
	server := feedServer(t, func(ctx context.Context, c *websocket.Conn) { <-ctx.Done() })
	h := newHarness(t, map[string]http.Handler{"unused.test": http.NotFoundHandler()})
	h.useFeedServer(server)
	sess := openFeedSession(t, h)

	for _, raw := range []string{
		"https://feed.test/app/key", // a fetch scheme is not a feed scheme
		"ws://feed.test/app/key",    // cleartext
		"wss://other.test/app/key",  // off the session allowlist
		"wss://feed.test:8443/app",  // non-443 port
	} {
		if _, err := sess.Subscribe(h.ctxHello(), raw, SubscribeOptions{}); !errors.Is(err, ErrDenied) {
			t.Errorf("Subscribe(%q) error = %v, want ErrDenied", raw, err)
		}
	}
}

func TestSubscribeRejectsUndeclarableFrames(t *testing.T) {
	server := feedServer(t, func(ctx context.Context, c *websocket.Conn) { <-ctx.Done() })
	h := newHarness(t, map[string]http.Handler{"unused.test": http.NotFoundHandler()})
	h.useFeedServer(server)
	sess := openFeedSession(t, h)

	cases := map[string]SubscribeOptions{
		"not json":         {Handshake: [][]byte{[]byte("subscribe me")}},
		"empty frame":      {Handshake: [][]byte{{}}},
		"oversized frame":  {Handshake: [][]byte{[]byte(`"` + strings.Repeat("x", maxHandshakeFrameBytes) + `"`)}},
		"too many frames":  {Handshake: make([][]byte, maxHandshakeFrames+1)},
		"unnamed keepaliv": {KeepAlive: []KeepAliveRule{{Reply: []byte(`{}`)}}},
		"unset reply":      {KeepAlive: []KeepAliveRule{{Event: "ping"}}},
	}
	for name, opts := range cases {
		if _, err := sess.Subscribe(h.ctxHello(), "wss://feed.test/app/key", opts); err == nil {
			t.Errorf("Subscribe(%s) succeeded, want rejection", name)
		}
	}
}

func TestSubscribeEndsWhenSessionCloses(t *testing.T) {
	server := feedServer(t, func(ctx context.Context, c *websocket.Conn) { <-ctx.Done() })
	h := newHarness(t, map[string]http.Handler{"unused.test": http.NotFoundHandler()})
	h.useFeedServer(server)
	sess := openFeedSession(t, h)

	sub, err := sess.Subscribe(h.ctxHello(), "wss://feed.test/app/key", SubscribeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Close(h.ctxHello()); err != nil {
		t.Fatal(err)
	}
	select {
	case _, open := <-sub.Frames():
		if open {
			t.Fatal("frames still delivering after session close")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("frames channel not closed by session teardown")
	}
	if !errors.Is(sub.Err(), ErrClosed) {
		t.Fatalf("Err() = %v, want ErrClosed", sub.Err())
	}
}

func TestSubscribeEndsWhenPluginDisabled(t *testing.T) {
	server := feedServer(t, func(ctx context.Context, c *websocket.Conn) { <-ctx.Done() })
	h := newHarness(t, map[string]http.Handler{"unused.test": http.NotFoundHandler()})
	h.useFeedServer(server)
	sess := openFeedSession(t, h)

	sub, err := sess.Subscribe(h.ctxHello(), "wss://feed.test/app/key", SubscribeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.pol.Disable(h.ctx, "hello", "test", "kill switch"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, open := <-sub.Frames():
			if open {
				continue
			}
			return
		case <-deadline:
			t.Fatal("disable did not close the subscription")
		}
	}
}

func TestSubscribeEnforcesPerSessionLimit(t *testing.T) {
	server := feedServer(t, func(ctx context.Context, c *websocket.Conn) { <-ctx.Done() })
	h := newHarness(t, map[string]http.Handler{"unused.test": http.NotFoundHandler()})
	h.useFeedServer(server)
	sess := openFeedSession(t, h)

	for i := 0; i < h.svc.opts.MaxSubscriptionsPerSession; i++ {
		if _, err := sess.Subscribe(h.ctxHello(), "wss://feed.test/app/key", SubscribeOptions{}); err != nil {
			t.Fatalf("subscription %d: %v", i, err)
		}
	}
	if _, err := sess.Subscribe(h.ctxHello(), "wss://feed.test/app/key", SubscribeOptions{}); !errors.Is(err, ErrLimit) {
		t.Fatalf("error = %v, want ErrLimit", err)
	}
}

func TestSubscribeRequiresPluginIdentity(t *testing.T) {
	server := feedServer(t, func(ctx context.Context, c *websocket.Conn) { <-ctx.Done() })
	h := newHarness(t, map[string]http.Handler{"unused.test": http.NotFoundHandler()})
	h.useFeedServer(server)
	sess := openFeedSession(t, h)

	if _, err := sess.Subscribe(h.ctx, "wss://feed.test/app/key", SubscribeOptions{}); !errors.Is(err, ErrNoPlugin) {
		t.Fatalf("error = %v, want ErrNoPlugin", err)
	}
}

func TestDialCheckedRejectsPrivateAndNon443(t *testing.T) {
	ctx := context.Background()
	for _, addr := range []string{"localhost:443", "feed.test:8443", "127.0.0.1:443", "[::1]:443"} {
		if _, err := dialChecked(ctx, "tcp", addr); !errors.Is(err, ErrDenied) {
			t.Errorf("dialChecked(%q) error = %v, want ErrDenied", addr, err)
		}
	}
}
