package browser

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// A subscription is the read side of a realtime feed: the host dials an allowlisted
// wss:// endpoint, sends the handshake the plugin declared up front, and streams the
// text frames back. There is deliberately no send channel. A plugin that could write
// arbitrary frames after connect would have the general-purpose socket that Post's
// form-only body already refuses it, and the allowlist would stop being the whole
// story about what the session can do.
//
// The dial does not go through the engine. Chromium is not involved and neither is the
// page cookie jar, so a subscription reaches exactly one host with no ambient
// credentials -- which also means it can only join channels that need no authenticated
// handshake.

const (
	defaultMaxSubscriptionsPerSession = 4
	defaultMaxFrameBytes              = 1 << 20
	defaultFrameBuffer                = 256
	defaultSubscribeTimeout           = 30 * time.Second
	// A multiplexed protocol joins every channel over one socket, so the handshake is
	// one small frame per channel: an auction's worth of lots, not a handful.
	maxHandshakeFrames     = 512
	maxHandshakeFrameBytes = 4 << 10
	maxHandshakeTotalBytes = 256 << 10
	maxKeepAliveRules      = 8
)

// ErrSubscriptionClosed is the Err of a subscription the plugin closed itself.
var ErrSubscriptionClosed = errors.New("browser: subscription closed")

func (s *session) Subscribe(ctx context.Context, raw string, opts SubscribeOptions) (Subscription, error) {
	if _, err := pluginID(ctx); err != nil {
		return nil, err
	}
	if err := s.admit(ctx); err != nil {
		return nil, err
	}
	if err := validateSubscribeOptions(opts); err != nil {
		return nil, err
	}

	u, err := parsePageURL(raw)
	if err != nil {
		s.subDenied(ctx, nil, err)
		return nil, err
	}
	if err := checkURLScheme(u, s.allowed, "wss"); err != nil {
		s.subDenied(ctx, u, err)
		return nil, err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	if len(s.subs) >= s.svc.opts.MaxSubscriptionsPerSession {
		s.mu.Unlock()
		return nil, ErrLimit
	}
	s.mu.Unlock()

	dialCtx, cancelDial := context.WithTimeout(ctx, s.svc.opts.SubscribeTimeout)
	defer cancelDial()
	conn, _, err := websocket.Dial(dialCtx, u.String(), &websocket.DialOptions{
		HTTPClient: s.svc.subscribeClient(),
	})
	if err != nil {
		if s.ctx.Err() != nil {
			return nil, ErrClosed
		}
		if errors.Is(err, ErrDenied) {
			s.subDenied(ctx, u, err)
			return nil, err
		}
		if dialCtx.Err() != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrEngine, err)
	}
	conn.SetReadLimit(int64(s.svc.opts.MaxFrameBytes))

	// The run context outlives the caller's ctx: Subscribe returns while the socket
	// stays open. It is a child of the session, so plugin disable and Session.Close
	// both tear the connection down.
	runCtx, cancel := context.WithCancel(s.ctx)
	sub := &subscription{
		sess:   s,
		conn:   conn,
		url:    u.Redacted(),
		ctx:    runCtx,
		cancel: cancel,
		frames: make(chan Frame, defaultFrameBuffer),
		rules:  opts.KeepAlive,
	}

	for _, frame := range opts.Handshake {
		if err := conn.Write(dialCtx, websocket.MessageText, frame); err != nil {
			cancel()
			_ = conn.CloseNow()
			return nil, fmt.Errorf("%w: handshake: %v", ErrEngine, err)
		}
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		_ = conn.CloseNow()
		return nil, ErrClosed
	}
	s.subs = append(s.subs, sub)
	s.mu.Unlock()

	s.svc.opts.Log.Info("browser: subscription opened",
		"plugin", s.pluginID, "job", s.jobID, "url", sub.url)
	go sub.read()
	return sub, nil
}

func validateSubscribeOptions(opts SubscribeOptions) error {
	if len(opts.Handshake) > maxHandshakeFrames {
		return fmt.Errorf("%w: %d handshake frames (max %d)", ErrLimit, len(opts.Handshake), maxHandshakeFrames)
	}
	total := 0
	for _, frame := range opts.Handshake {
		if len(frame) == 0 || len(frame) > maxHandshakeFrameBytes {
			return fmt.Errorf("%w: handshake frame must be 1..%d bytes", ErrLimit, maxHandshakeFrameBytes)
		}
		if !json.Valid(frame) {
			return fmt.Errorf("%w: handshake frame is not JSON", ErrEngine)
		}
		total += len(frame)
	}
	if total > maxHandshakeTotalBytes {
		return fmt.Errorf("%w: handshake is %d bytes (max %d)", ErrLimit, total, maxHandshakeTotalBytes)
	}
	if len(opts.KeepAlive) > maxKeepAliveRules {
		return fmt.Errorf("%w: %d keepalive rules (max %d)", ErrLimit, len(opts.KeepAlive), maxKeepAliveRules)
	}
	for _, rule := range opts.KeepAlive {
		if rule.Event == "" {
			return fmt.Errorf("%w: keepalive rule needs an event", ErrEngine)
		}
		if len(rule.Reply) == 0 || len(rule.Reply) > maxHandshakeFrameBytes || !json.Valid(rule.Reply) {
			return fmt.Errorf("%w: keepalive reply must be JSON under %d bytes", ErrEngine, maxHandshakeFrameBytes)
		}
	}
	return nil
}

func (s *session) subDenied(ctx context.Context, u *url.URL, err error) {
	var d *denial
	if errors.As(err, &d) {
		if d.reported {
			return
		}
		d.reported = true
	}
	s.svc.opts.Log.Warn("browser: url denied",
		"plugin", s.pluginID, "job", s.jobID, "stage", "subscribe",
		"host", deniedHost(u), "reason", deniedReason(err), "err", err)
	s.svc.deny(ctx, s.pluginID, s.jobID, deniedHost(u), deniedReason(err))
}

func (s *session) dropSubscription(sub *subscription) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.subs[:0]
	for _, x := range s.subs {
		if x != sub {
			out = append(out, x)
		}
	}
	s.subs = out
}

// subscribeClient dials only checked public addresses on 443. The websocket handshake
// is an ordinary HTTPS request, so the same connect-time gate the CONNECT proxy gives
// Chromium is applied here in the dialer.
func (s *Service) subscribeClient() *http.Client {
	if s.opts.SubscribeClient != nil {
		return s.opts.SubscribeClient
	}
	return &http.Client{
		Transport: &http.Transport{
			DialContext:     dialChecked,
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		},
	}
}

func dialChecked(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, denyTarget("address", addr)
	}
	if port != "443" {
		return nil, denyTarget("port", addr+" (only 443 is allowed)")
	}
	if ip := net.ParseIP(host); ip != nil {
		return nil, denyTarget("address", host+" is a literal IP; only DNS names may be reached")
	}
	ips, err := defaultLookup(ctx, host)
	if err != nil {
		return nil, denyTarget("address", host+" did not resolve: "+err.Error())
	}
	if len(ips) == 0 {
		return nil, denyTarget("address", host+" resolved to no addresses")
	}
	for _, ip := range ips {
		if err := checkResolvedIP(ip); err != nil {
			return nil, err
		}
	}
	var last error
	for _, ip := range ips {
		c, err := defaultDial(ctx, ip, port)
		if err == nil {
			return c, nil
		}
		last = err
	}
	if last == nil {
		return nil, denyTarget("address", host+" had no dialable address")
	}
	return nil, last
}

type subscription struct {
	sess  *session
	conn  *websocket.Conn
	url   string
	rules []KeepAliveRule

	ctx    context.Context
	cancel context.CancelFunc
	frames chan Frame

	mu      sync.Mutex
	err     error
	closed  bool
	dropped int
}

func (s *subscription) Frames() <-chan Frame { return s.frames }

func (s *subscription) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *subscription) Close(ctx context.Context) error {
	s.finish(ErrSubscriptionClosed)
	return nil
}

// finish records why the socket ended, tears it down, and closes Frames exactly once.
// The reader and the plugin race to call it; the first cause wins.
func (s *subscription) finish(cause error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if s.err == nil {
		s.err = cause
	}
	dropped := s.dropped
	s.mu.Unlock()

	s.cancel()
	_ = s.conn.CloseNow()
	close(s.frames)
	s.sess.dropSubscription(s)
	s.sess.svc.opts.Log.Info("browser: subscription closed",
		"plugin", s.sess.pluginID, "job", s.sess.jobID,
		"url", s.url, "dropped", dropped, "reason", cause)
}

func (s *subscription) read() {
	for {
		kind, data, err := s.conn.Read(s.ctx)
		if err != nil {
			cause := err
			if s.ctx.Err() != nil {
				cause = ErrClosed
			}
			s.finish(cause)
			return
		}
		if kind != websocket.MessageText {
			continue
		}
		s.answer(data)
		select {
		case s.frames <- Frame{At: time.Now().UTC(), Data: data}:
		default:
			// A plugin that stops draining must not grow the host's heap or stall the
			// socket into a server-side disconnect. Newest frame is dropped and counted.
			s.mu.Lock()
			s.dropped++
			n := s.dropped
			s.mu.Unlock()
			if n == 1 || n%100 == 0 {
				s.sess.svc.opts.Log.Warn("browser: subscription frames dropped",
					"plugin", s.sess.pluginID, "url", s.url, "dropped", n)
			}
		}
	}
}

// answer replies to a heartbeat the plugin declared at subscribe time. Only the
// declared reply is ever written, so this is not a send channel by another name.
func (s *subscription) answer(data []byte) {
	if len(s.rules) == 0 {
		return
	}
	var envelope struct {
		Event string `json:"event"`
	}
	if json.Unmarshal(data, &envelope) != nil || envelope.Event == "" {
		return
	}
	for _, rule := range s.rules {
		if rule.Event != envelope.Event {
			continue
		}
		if err := s.conn.Write(s.ctx, websocket.MessageText, rule.Reply); err != nil {
			s.sess.svc.opts.Log.Warn("browser: keepalive write failed",
				"plugin", s.sess.pluginID, "url", s.url, "event", rule.Event, "err", err)
		}
		return
	}
}
