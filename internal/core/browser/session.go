package browser

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

type session struct {
	svc      *Service
	pluginID string
	jobID    string
	allowed  allowlist

	ctx    context.Context
	cancel context.CancelFunc
	engine EngineSession

	mu     sync.Mutex
	pages  []*page
	subs   []*subscription
	closed bool
}

func (s *session) pageCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, p := range s.pages {
		if p != nil && !p.closed {
			n++
		}
	}
	return n
}

func (s *session) admit(ctx context.Context) error {
	if err := s.svc.gate.CheckWork(ctx, s.pluginID); err != nil {
		return err
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return ErrClosed
	}
	select {
	case <-s.ctx.Done():
		return ErrClosed
	default:
	}
	return nil
}

func (s *session) NewPage(ctx context.Context) (Page, error) {
	if _, err := pluginID(ctx); err != nil {
		return nil, err
	}
	if err := s.admit(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	n := 0
	for _, p := range s.pages {
		if p != nil && !p.closed {
			n++
		}
	}
	if n >= s.svc.opts.MaxPagesPerSession {
		s.mu.Unlock()
		return nil, ErrLimit
	}
	s.mu.Unlock()

	s.svc.mu.Lock()
	hostPages := s.svc.pageCountLocked()
	s.svc.mu.Unlock()
	if hostPages >= s.svc.opts.MaxPagesHost {
		return nil, ErrLimit
	}

	eng, err := s.engine.NewPage(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrEngine, err)
	}
	p := &page{sess: s, engine: eng}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = eng.Close(ctx)
		return nil, ErrClosed
	}
	s.pages = append(s.pages, p)
	s.mu.Unlock()
	return p, nil
}

func (s *session) Close(ctx context.Context) error {
	return s.closeLocked(ctx)
}

func (s *session) closeLocked(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.cancel()
	pages := s.pages
	s.pages = nil
	subs := s.subs
	s.subs = nil
	eng := s.engine
	s.mu.Unlock()

	for _, sub := range subs {
		sub.finish(ErrClosed)
	}
	for _, p := range pages {
		_ = p.closeEngine(ctx)
	}
	var err error
	if eng != nil {
		err = eng.Close(ctx)
	}
	s.svc.dropSession(s)
	return err
}

type page struct {
	sess   *session
	engine EnginePage

	mu     sync.Mutex
	html   string
	closed bool
}

func (p *page) admit(ctx context.Context) error {
	if _, err := pluginID(ctx); err != nil {
		return err
	}
	if err := p.sess.admit(ctx); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrClosed
	}
	return nil
}

func (p *page) opCtx(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	op, cancel := context.WithTimeout(ctx, timeout)
	stop := context.AfterFunc(p.sess.ctx, cancel)
	return op, func() {
		stop()
		cancel()
	}
}

func (p *page) check(ctx context.Context, raw string) (*url.URL, error) {
	u, err := parsePageURL(raw)
	if err != nil {
		p.denied(ctx, "start url", nil, err)
		return nil, err
	}
	if err := checkURL(u, p.sess.allowed); err != nil {
		p.denied(ctx, "start url", u, err)
		return nil, err
	}
	return u, nil
}

// denied logs the rejection with the URL and the stage that produced it, then
// publishes the audit event. The plugin only sees the error, so the log is the one
// place that says which of a page's many requests was blocked.
func (p *page) denied(ctx context.Context, stage string, u *url.URL, err error) {
	var d *denial
	if errors.As(err, &d) {
		if d.reported {
			return
		}
		d.reported = true
		if u == nil && d.target != "" {
			if parsed, perr := url.Parse(d.target); perr == nil {
				u = parsed
			}
		}
	}
	p.sess.svc.opts.Log.Warn("browser: url denied",
		"plugin", p.sess.pluginID,
		"job", p.sess.jobID,
		"stage", stage,
		"host", deniedHost(u),
		"reason", deniedReason(err),
		"err", err)
	p.sess.svc.deny(ctx, p.sess.pluginID, p.sess.jobID, deniedHost(u), deniedReason(err))
}

func (p *page) Goto(ctx context.Context, raw string) error {
	if err := p.admit(ctx); err != nil {
		return err
	}
	u, err := p.check(ctx, raw)
	if err != nil {
		return err
	}
	op, cancel := p.opCtx(ctx, p.sess.svc.opts.NavigationTimeout)
	defer cancel()
	doc, final, err := p.engine.Goto(op, u, func(next *url.URL) error {
		return p.gateURL(ctx, next)
	})
	p.logBlocked(ctx)
	if p.sess.ctx.Err() != nil {
		return ErrClosed
	}
	if err != nil {
		if op.Err() != nil && !errors.Is(err, ErrDenied) {
			return op.Err()
		}
		if errors.Is(err, ErrDenied) {
			p.denied(ctx, "engine", nil, err)
		}
		return err
	}
	base := final
	if base == nil {
		base = u
	}
	p.sess.svc.opts.Log.Debug("browser: navigated",
		"plugin", p.sess.pluginID,
		"job", p.sess.jobID,
		"url", u.Redacted(),
		"final", base.Redacted(),
		"bytes", len(doc))
	if err := p.checkSubresources(ctx, base, doc); err != nil {
		return err
	}
	p.mu.Lock()
	p.html = doc
	p.mu.Unlock()
	return nil
}

func (p *page) gateURL(ctx context.Context, u *url.URL) error {
	if err := checkURL(u, p.sess.allowed); err != nil {
		p.denied(ctx, "request", u, err)
		return err
	}
	return nil
}

// logBlocked reports subresources the engine aborted during the last operation. The
// page rendered without them, so this is the only trace that a host is missing from
// the session allowlist -- the place to look when a page half-loads.
func (p *page) logBlocked(ctx context.Context) {
	r, ok := p.engine.(blockReporter)
	if !ok {
		return
	}
	for _, b := range r.TakeBlocked() {
		p.sess.svc.opts.Log.Warn("browser: subresource blocked",
			"plugin", p.sess.pluginID,
			"job", p.sess.jobID,
			"url", b.URL,
			"reason", b.Reason,
			"err", b.Err)
		p.sess.svc.deny(ctx, p.sess.pluginID, p.sess.jobID, blockedHost(b.URL), b.Reason)
	}
}

func blockedHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return deniedHost(u)
}

// gatesRequests reports whether the engine allowlist-checks every network request it
// makes. An engine that does (Playwright, via route interception) has already blocked
// any off-allowlist subresource, so re-scanning the rendered DOM would only fail the
// navigation on references the browser never fetched.
func (p *page) gatesRequests() bool {
	g, ok := p.engine.(requestGater)
	return ok && g.GatesRequests()
}

func (p *page) checkSubresources(ctx context.Context, pageURL *url.URL, doc string) error {
	if p.gatesRequests() {
		return nil
	}
	refs, err := resourceRefs(doc)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrEngine, err)
	}
	for _, ref := range refs {
		abs, err := pageURL.Parse(ref)
		if err != nil {
			err := denyTarget("scheme", ref+" is not a parseable reference")
			p.denied(ctx, "subresource", nil, err)
			return err
		}
		if abs.Scheme == "" {
			abs.Scheme = "https"
		}
		if !networkFetch(abs) {
			continue
		}
		if err := p.gateURL(ctx, abs); err != nil {
			return err
		}
	}
	return nil
}

func networkFetch(u *url.URL) bool {
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "ws", "wss", "ftp", "file":
		return true
	}
	return false
}

func (p *page) WaitFor(ctx context.Context, selector string, d time.Duration) error {
	if err := p.admit(ctx); err != nil {
		return err
	}
	if d <= 0 || d > p.sess.svc.opts.WaitForCap {
		d = p.sess.svc.opts.WaitForCap
	}
	op, cancel := p.opCtx(ctx, d)
	defer cancel()

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		doc, err := p.document(op)
		if err != nil {
			return err
		}
		ok, err := htmlMatches(doc, selector)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrEngine, err)
		}
		if ok {
			return nil
		}
		select {
		case <-op.Done():
			return op.Err()
		case <-ticker.C:
		}
	}
}

func (p *page) Fill(ctx context.Context, selector, value string) error {
	if err := p.admit(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(selector) == "" {
		return fmt.Errorf("%w: empty selector", ErrEngine)
	}
	op, cancel := p.opCtx(ctx, p.sess.svc.opts.NavigationTimeout)
	defer cancel()
	if err := p.engine.Fill(op, selector, value); err != nil {
		if p.sess.ctx.Err() != nil {
			return ErrClosed
		}
		if op.Err() != nil && !errors.Is(err, ErrDenied) {
			return op.Err()
		}
		return err
	}
	return nil
}

func (p *page) Click(ctx context.Context, selector string) error {
	if err := p.admit(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(selector) == "" {
		return fmt.Errorf("%w: empty selector", ErrEngine)
	}
	op, cancel := p.opCtx(ctx, p.sess.svc.opts.NavigationTimeout)
	defer cancel()
	doc, final, err := p.engine.Click(op, selector, func(next *url.URL) error {
		return p.gateURL(ctx, next)
	})
	p.logBlocked(ctx)
	if p.sess.ctx.Err() != nil {
		return ErrClosed
	}
	if err != nil {
		if op.Err() != nil && !errors.Is(err, ErrDenied) {
			return op.Err()
		}
		if errors.Is(err, ErrDenied) {
			p.denied(ctx, "engine", nil, err)
		}
		return err
	}
	if doc == "" && final == nil {
		return nil
	}
	if final != nil {
		if err := p.gateURL(ctx, final); err != nil {
			return err
		}
		if err := p.checkSubresources(ctx, final, doc); err != nil {
			return err
		}
	}
	if doc != "" {
		p.mu.Lock()
		p.html = doc
		p.mu.Unlock()
	}
	return nil
}

func (p *page) Content(ctx context.Context) (string, error) {
	if err := p.admit(ctx); err != nil {
		return "", err
	}
	doc, err := p.document(ctx)
	if err != nil {
		return "", err
	}
	if len(doc) > p.sess.svc.opts.MaxDocumentBytes {
		return "", ErrLimit
	}
	return doc, nil
}

// liveHTML is implemented by engines that keep a live document (Playwright).
type liveHTML interface {
	Content(ctx context.Context) (string, error)
}

func (p *page) document(ctx context.Context) (string, error) {
	if live, ok := p.engine.(liveHTML); ok {
		doc, err := live.Content(ctx)
		if err != nil {
			if p.sess.ctx.Err() != nil {
				return "", ErrClosed
			}
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", fmt.Errorf("%w: %v", ErrEngine, err)
		}
		return doc, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.html, nil
}

func (p *page) Get(ctx context.Context, raw string) (Resource, error) {
	if err := p.admit(ctx); err != nil {
		return Resource{}, err
	}
	u, err := p.check(ctx, raw)
	if err != nil {
		return Resource{}, err
	}
	op, cancel := p.opCtx(ctx, p.sess.svc.opts.NavigationTimeout)
	defer cancel()
	res, err := p.engine.Get(op, u, func(next *url.URL) error {
		return p.gateURL(ctx, next)
	})
	if err != nil {
		if op.Err() != nil && !errors.Is(err, ErrDenied) {
			return Resource{}, op.Err()
		}
		return Resource{}, err
	}
	if len(res.Body) > p.sess.svc.opts.MaxResourceBytes {
		return Resource{}, ErrLimit
	}
	return res, nil
}

func (p *page) Post(ctx context.Context, raw string, form url.Values) (Resource, error) {
	if err := p.admit(ctx); err != nil {
		return Resource{}, err
	}
	u, err := p.check(ctx, raw)
	if err != nil {
		return Resource{}, err
	}
	if form == nil {
		form = url.Values{}
	}
	op, cancel := p.opCtx(ctx, p.sess.svc.opts.NavigationTimeout)
	defer cancel()
	res, err := p.engine.Post(op, u, form, func(next *url.URL) error {
		return p.gateURL(ctx, next)
	})
	if err != nil {
		if op.Err() != nil && !errors.Is(err, ErrDenied) {
			return Resource{}, op.Err()
		}
		return Resource{}, err
	}
	if len(res.Body) > p.sess.svc.opts.MaxResourceBytes {
		return Resource{}, ErrLimit
	}
	return res, nil
}

func (p *page) Responses(ctx context.Context) ([]Resource, error) {
	if err := p.admit(ctx); err != nil {
		return nil, err
	}
	out := p.engine.Resources()
	kept := out[:0]
	for _, r := range out {
		if len(r.Body) > p.sess.svc.opts.MaxResourceBytes {
			continue
		}
		kept = append(kept, r)
	}
	if len(kept) == 0 {
		return nil, nil
	}
	return kept, nil
}

func (p *page) Close(ctx context.Context) error {
	return p.closeEngine(ctx)
}

func (p *page) closeEngine(ctx context.Context) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	eng := p.engine
	p.mu.Unlock()
	if eng == nil {
		return nil
	}
	return eng.Close(ctx)
}

func resourceRefs(doc string) ([]string, error) {
	root, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		return nil, err
	}
	var refs []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "img", "script", "iframe", "source", "video", "audio":
				if v := attr(n, "src"); v != "" {
					refs = append(refs, v)
				}
			case "link":
				if v := attr(n, "href"); v != "" {
					refs = append(refs, v)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return refs, nil
}

func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, name) {
			return a.Val
		}
	}
	return ""
}

func hasClass(n *html.Node, class string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == class {
			return true
		}
	}
	return false
}
