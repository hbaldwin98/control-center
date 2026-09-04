// Package browser owns headless sessions for plugins: launch, allowlist, fetch, and
// tear down. Plugins never import an automation library.
//
// Layer 3. It imports events and policy. The engine (Fake today) is an implementation
// detail; the plugin-facing API is the stable surface.
package browser

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/policy"
)

// Errors returned across the browser boundary.
var (
	ErrInvalidAllowlist = errors.New("browser: allowlist is empty or invalid")
	ErrDenied           = errors.New("browser: url denied")
	ErrLimit            = errors.New("browser: session or page limit")
	ErrEngine           = errors.New("browser: engine failed")
	ErrReader           = errors.New("browser: reader unavailable")
	ErrNoPlugin         = errors.New("browser: plugin identity missing")
	ErrClosed           = errors.New("browser: session closed")
)

const (
	defaultSessionsPerPlugin = 2
	defaultPagesPerSession   = 4
	defaultPagesHost         = 32
	defaultMaxDocument       = 5 << 20
	defaultMaxResource       = 10 << 20
	defaultNavTimeout        = 30 * time.Second
	defaultWaitCap           = 60 * time.Second
)

// Options configures limits and the engine. Zero values take the spec defaults.
type Options struct {
	Engine Engine
	Reader Reader
	Log    *slog.Logger
	Client *http.Client

	MaxSessionsPerPlugin int
	MaxPagesPerSession   int
	MaxPagesHost         int
	MaxDocumentBytes     int
	MaxResourceBytes     int
	NavigationTimeout    time.Duration
	WaitForCap           time.Duration

	MaxSubscriptionsPerSession int
	MaxFrameBytes              int
	SubscribeTimeout           time.Duration
	// SubscribeClient overrides the websocket handshake client. The default dials only
	// checked public addresses on 443; a test supplies its own to reach a local server.
	SubscribeClient *http.Client
}

// Read renders an allowlisted page through the browser engine, then gives only its
// HTML to the configured reader. The reader never becomes a second URL fetcher.
func (s *Service) Read(ctx context.Context, opts OpenOptions, rawURL string) (Document, error) {
	pluginID, err := pluginID(ctx)
	if err != nil {
		return Document{}, err
	}
	if err := s.gate.CheckWork(ctx, pluginID); err != nil {
		return Document{}, err
	}
	if s.opts.Reader == nil {
		return Document{}, ErrReader
	}
	sess, err := s.Open(ctx, opts)
	if err != nil {
		return Document{}, err
	}
	defer func() { _ = sess.Close(context.Background()) }()
	page, err := sess.NewPage(ctx)
	if err != nil {
		return Document{}, err
	}
	if err := page.Goto(ctx, rawURL); err != nil {
		return Document{}, err
	}
	html, err := page.Content(ctx)
	if err != nil {
		return Document{}, err
	}
	content, err := s.opts.Reader.Extract(ctx, html)
	if err != nil {
		if ctx.Err() != nil {
			return Document{}, ctx.Err()
		}
		return Document{}, fmt.Errorf("%w: %v", ErrReader, err)
	}
	return Document{URL: rawURL, Content: content}, nil
}

func (o *Options) applyDefaults() {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: defaultNavTimeout}
	}
	if o.MaxSessionsPerPlugin <= 0 {
		o.MaxSessionsPerPlugin = defaultSessionsPerPlugin
	}
	if o.MaxPagesPerSession <= 0 {
		o.MaxPagesPerSession = defaultPagesPerSession
	}
	if o.MaxPagesHost <= 0 {
		o.MaxPagesHost = defaultPagesHost
	}
	if o.MaxDocumentBytes <= 0 {
		o.MaxDocumentBytes = defaultMaxDocument
	}
	if o.MaxResourceBytes <= 0 {
		o.MaxResourceBytes = defaultMaxResource
	}
	if o.NavigationTimeout <= 0 {
		o.NavigationTimeout = defaultNavTimeout
	}
	if o.WaitForCap <= 0 {
		o.WaitForCap = defaultWaitCap
	}
	if o.MaxSubscriptionsPerSession <= 0 {
		o.MaxSubscriptionsPerSession = defaultMaxSubscriptionsPerSession
	}
	if o.MaxFrameBytes <= 0 {
		o.MaxFrameBytes = defaultMaxFrameBytes
	}
	if o.SubscribeTimeout <= 0 {
		o.SubscribeTimeout = defaultSubscribeTimeout
	}
}

// Do performs an allowlisted HTTP request without launching a browser engine.
func (s *Service) Do(ctx context.Context, opts OpenOptions, request Request) (Resource, error) {
	pluginID, err := pluginID(ctx)
	if err != nil {
		return Resource{}, err
	}
	if err := s.gate.CheckWork(ctx, pluginID); err != nil {
		return Resource{}, err
	}
	allowed, err := parseAllowlist(opts.AllowedHosts)
	if err != nil {
		return Resource{}, err
	}
	u, err := parsePageURL(request.URL)
	if err != nil {
		return Resource{}, err
	}
	if err := checkURL(u, allowed); err != nil {
		s.deny(ctx, pluginID, jobID(ctx), deniedHost(u), deniedReason(err))
		return Resource{}, err
	}
	req, err := http.NewRequestWithContext(ctx, request.Method, u.String(), bytes.NewReader(request.Body))
	if err != nil {
		return Resource{}, fmt.Errorf("%w: %v", ErrEngine, err)
	}
	for name, value := range request.Headers {
		req.Header.Set(name, value)
	}
	client := *s.opts.Client
	priorRedirect := client.CheckRedirect
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if err := checkURL(next.URL, allowed); err != nil {
			s.deny(ctx, pluginID, jobID(ctx), deniedHost(next.URL), deniedReason(err))
			return err
		}
		if priorRedirect != nil {
			return priorRedirect(next, via)
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Resource{}, ctx.Err()
		}
		if errors.Is(err, ErrDenied) {
			return Resource{}, err
		}
		return Resource{}, fmt.Errorf("%w: %v", ErrEngine, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(s.opts.MaxResourceBytes)+1))
	if err != nil {
		return Resource{}, fmt.Errorf("%w: %v", ErrEngine, err)
	}
	if len(body) > s.opts.MaxResourceBytes {
		return Resource{}, ErrLimit
	}
	return Resource{URL: resp.Request.URL.String(), MIME: resp.Header.Get("Content-Type"), Body: body, Status: resp.StatusCode}, nil
}

// Service is the concrete unscoped browser capability.
type Service struct {
	bus  events.Bus
	gate policy.Gate
	opts Options

	mu       sync.Mutex
	sessions map[string][]*session // plugin ID

	unwatch func()
}

// New starts watching policy so disable closes admitted sessions.
func New(bus events.Bus, gate policy.Gate, opts Options) (*Service, error) {
	if opts.Engine == nil {
		return nil, fmt.Errorf("browser: engine is required")
	}
	if gate == nil {
		return nil, fmt.Errorf("browser: policy gate is required")
	}
	opts.applyDefaults()
	s := &Service{
		bus:      bus,
		gate:     gate,
		opts:     opts,
		sessions: map[string][]*session{},
	}
	s.unwatch = gate.Watch(func(pluginID string, enabled bool) {
		if !enabled {
			_ = s.ClosePlugin(context.Background(), pluginID)
		}
	})
	return s, nil
}

// Close stops the policy watch and tears down every session.
func (s *Service) Close() {
	if s.unwatch != nil {
		s.unwatch()
		s.unwatch = nil
	}
	s.mu.Lock()
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		_ = s.ClosePlugin(context.Background(), id)
	}
	if c, ok := s.opts.Engine.(io.Closer); ok {
		_ = c.Close()
	}
}

// ClosePlugin cancels and closes every session for pluginID. Idempotent.
func (s *Service) ClosePlugin(ctx context.Context, pluginID string) error {
	s.mu.Lock()
	list := s.sessions[pluginID]
	delete(s.sessions, pluginID)
	s.mu.Unlock()
	var first error
	for _, sess := range list {
		if sess == nil {
			continue
		}
		if err := sess.closeLocked(ctx); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Open admits a session after CheckWork and allowlist validation.
func (s *Service) Open(ctx context.Context, opts OpenOptions) (Session, error) {
	pluginID, err := pluginID(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.gate.CheckWork(ctx, pluginID); err != nil {
		return nil, err
	}
	al, err := parseAllowlist(opts.AllowedHosts)
	if err != nil {
		return nil, err
	}
	if err := s.reserveSlot(pluginID); err != nil {
		return nil, err
	}

	eng, err := s.opts.Engine.NewSession(ctx, pluginID)
	if err != nil {
		s.releaseReservation(pluginID)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrEngine, err)
	}

	sessCtx, cancel := context.WithCancel(context.Background())
	sess := &session{
		svc:      s,
		pluginID: pluginID,
		jobID:    jobID(ctx),
		allowed:  al,
		ctx:      sessCtx,
		cancel:   cancel,
		engine:   eng,
	}
	if err := s.commitSession(pluginID, sess); err != nil {
		cancel()
		_ = eng.Close(ctx)
		return nil, err
	}
	s.opts.Log.Info("browser session opened", "plugin", pluginID, "job", sess.jobID)
	return sess, nil
}

func (s *Service) reserveSlot(pluginID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessions[pluginID]) >= s.opts.MaxSessionsPerPlugin {
		return ErrLimit
	}
	if s.pageCountLocked() >= s.opts.MaxPagesHost {
		return ErrLimit
	}
	// Placeholder so a concurrent Open cannot slip through before commitSession.
	s.sessions[pluginID] = append(s.sessions[pluginID], nil)
	return nil
}

func (s *Service) releaseReservation(pluginID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.sessions[pluginID]
	for i := len(list) - 1; i >= 0; i-- {
		if list[i] == nil {
			s.sessions[pluginID] = append(list[:i], list[i+1:]...)
			if len(s.sessions[pluginID]) == 0 {
				delete(s.sessions, pluginID)
			}
			return
		}
	}
}

func (s *Service) commitSession(pluginID string, sess *session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.sessions[pluginID]
	for i, existing := range list {
		if existing == nil {
			list[i] = sess
			s.sessions[pluginID] = list
			return nil
		}
	}
	return ErrClosed
}

func (s *Service) dropSession(sess *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.sessions[sess.pluginID]
	out := list[:0]
	for _, x := range list {
		if x != sess {
			out = append(out, x)
		}
	}
	if len(out) == 0 {
		delete(s.sessions, sess.pluginID)
		return
	}
	s.sessions[sess.pluginID] = out
}

func (s *Service) pageCountLocked() int {
	n := 0
	for _, list := range s.sessions {
		for _, sess := range list {
			if sess == nil {
				continue
			}
			n += sess.pageCount()
		}
	}
	return n
}

func (s *Service) deny(ctx context.Context, pluginID, jobID, host, reason string) {
	if s.bus == nil {
		return
	}
	payload := map[string]any{"plugin": pluginID, "host": host, "reason": reason}
	if jobID != "" {
		payload["job"] = jobID
	}
	if _, err := s.bus.Publish(ctx, events.Input{
		Type:    events.TypeBrowserDenied,
		Source:  events.SourceBrowser,
		Subject: pluginID,
		Payload: payload,
	}); err != nil {
		s.opts.Log.Error("browser: denied event", "err", err)
	}
}
