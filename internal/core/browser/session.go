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
	eng := s.engine
	s.mu.Unlock()

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
		p.sess.svc.deny(ctx, p.sess.pluginID, p.sess.jobID, "", "scheme")
		return nil, err
	}
	if err := checkURL(u, p.sess.allowed); err != nil {
		p.sess.svc.deny(ctx, p.sess.pluginID, p.sess.jobID, deniedHost(u), deniedReason(err))
		return nil, err
	}
	return u, nil
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
	if p.sess.ctx.Err() != nil {
		return ErrClosed
	}
	if err != nil {
		if op.Err() != nil && !errors.Is(err, ErrDenied) {
			return op.Err()
		}
		return err
	}
	base := final
	if base == nil {
		base = u
	}
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
		p.sess.svc.deny(ctx, p.sess.pluginID, p.sess.jobID, deniedHost(u), deniedReason(err))
		return err
	}
	return nil
}

func (p *page) checkSubresources(ctx context.Context, pageURL *url.URL, doc string) error {
	refs, err := resourceRefs(doc)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrEngine, err)
	}
	for _, ref := range refs {
		abs, err := pageURL.Parse(ref)
		if err != nil {
			p.sess.svc.deny(ctx, p.sess.pluginID, p.sess.jobID, "", "scheme")
			return fmt.Errorf("%w: scheme", ErrDenied)
		}
		if abs.Scheme == "" {
			abs.Scheme = "https"
		}
		if err := p.gateURL(ctx, abs); err != nil {
			return err
		}
	}
	return nil
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
		p.mu.Lock()
		doc := p.html
		p.mu.Unlock()
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

func (p *page) Content(ctx context.Context) (string, error) {
	if err := p.admit(ctx); err != nil {
		return "", err
	}
	p.mu.Lock()
	doc := p.html
	p.mu.Unlock()
	if len(doc) > p.sess.svc.opts.MaxDocumentBytes {
		return "", ErrLimit
	}
	return doc, nil
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

func htmlMatches(doc, selector string) (bool, error) {
	if selector == "" {
		return false, fmt.Errorf("empty selector")
	}
	tag, id, class := parseSelector(selector)
	root, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		return false, err
	}
	var found bool
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found {
			return
		}
		if n.Type == html.ElementNode {
			if (tag == "" || n.Data == tag) &&
				(id == "" || attr(n, "id") == id) &&
				(class == "" || hasClass(n, class)) {
				found = true
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return found, nil
}

func parseSelector(sel string) (tag, id, class string) {
	sel = strings.TrimSpace(sel)
	if strings.HasPrefix(sel, "#") {
		return "", strings.TrimPrefix(sel, "#"), ""
	}
	if strings.HasPrefix(sel, ".") {
		return "", "", strings.TrimPrefix(sel, ".")
	}
	if i := strings.IndexByte(sel, '#'); i >= 0 {
		return sel[:i], sel[i+1:], ""
	}
	if i := strings.IndexByte(sel, '.'); i >= 0 {
		return sel[:i], "", sel[i+1:]
	}
	return sel, "", ""
}

func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
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
