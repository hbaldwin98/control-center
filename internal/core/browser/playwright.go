package browser

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/mxschmitt/playwright-go"
)

// Playwright is the production engine: one Chromium for the process, isolated
// contexts per session, and connect-time SSRF checks through a loopback CONNECT
// proxy. There is no loopback or http:// exception.
type Playwright struct {
	mu      sync.Mutex
	pw      *playwright.Playwright
	browser playwright.Browser
	proxy   *ssrfProxy
	closed  bool
}

// NewPlaywright launches Chromium and the SSRF proxy. Missing driver/browsers are
// installed (Chromium only) on first start.
func NewPlaywright() (*Playwright, error) {
	return startPlaywright(true)
}

func startPlaywright(install bool) (*Playwright, error) {
	proxy, err := startSSRFProxy()
	if err != nil {
		return nil, fmt.Errorf("browser: ssrf proxy: %w", err)
	}
	pw, err := playwright.Run()
	if err != nil {
		if !install {
			_ = proxy.Close()
			return nil, err
		}
		if ierr := playwright.Install(&playwright.RunOptions{
			Browsers: []string{"chromium"},
			Verbose:  false,
		}); ierr != nil {
			_ = proxy.Close()
			return nil, fmt.Errorf("browser: install playwright: %w", ierr)
		}
		pw, err = playwright.Run()
		if err != nil {
			_ = proxy.Close()
			return nil, fmt.Errorf("browser: start playwright: %w", err)
		}
	}
	br, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless:      playwright.Bool(true),
		HandleSIGINT:  playwright.Bool(false),
		HandleSIGTERM: playwright.Bool(false),
		HandleSIGHUP:  playwright.Bool(false),
	})
	if err != nil {
		_ = pw.Stop()
		_ = proxy.Close()
		return nil, fmt.Errorf("browser: launch chromium: %w", err)
	}
	return &Playwright{pw: pw, browser: br, proxy: proxy}, nil
}

// Close tears down Chromium and the SSRF proxy. Idempotent.
func (e *Playwright) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	var first error
	if e.browser != nil {
		if err := e.browser.Close(); err != nil {
			first = err
		}
		e.browser = nil
	}
	if e.pw != nil {
		if err := e.pw.Stop(); err != nil && first == nil {
			first = err
		}
		e.pw = nil
	}
	if e.proxy != nil {
		if err := e.proxy.Close(); err != nil && first == nil {
			first = err
		}
		e.proxy = nil
	}
	return first
}

func (e *Playwright) NewSession(ctx context.Context, _ string) (EngineSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.browser == nil {
		return nil, ErrClosed
	}
	ctxpw, err := e.browser.NewContext(playwright.BrowserNewContextOptions{
		AcceptDownloads:   playwright.Bool(false),
		IgnoreHttpsErrors: playwright.Bool(false),
		JavaScriptEnabled: playwright.Bool(true),
		ServiceWorkers:    playwright.ServiceWorkerPolicyBlock,
		Proxy:             &playwright.Proxy{Server: e.proxy.URL()},
	})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = ctxpw.Close()
		return nil, err
	}
	return &pwSession{proxy: e.proxy, ctx: ctxpw}, nil
}

type pwSession struct {
	proxy  *ssrfProxy
	ctx    playwright.BrowserContext
	mu     sync.Mutex
	closed bool
}

func (s *pwSession) NewPage(ctx context.Context) (EnginePage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	page, err := s.ctx.NewPage()
	if err != nil {
		return nil, err
	}
	p := &pwPage{sess: s, page: page}
	if err := page.Route("**/*", p.onRoute); err != nil {
		_ = page.Close()
		return nil, err
	}
	page.OnResponse(p.onResponse)
	return p, nil
}

func (s *pwSession) Close(context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	ctx := s.ctx
	s.mu.Unlock()
	if ctx == nil {
		return nil
	}
	return ctx.Close()
}

type pwPage struct {
	sess *pwSession
	page playwright.Page

	mu       sync.Mutex
	check    func(*url.URL) error
	denied   error
	blocked  []Blocked
	closed   bool
	captured []Resource
}

// onRoute is the enforcement point: every request Chromium makes is allowlist- and
// SSRF-checked here, and anything that fails is aborted so it never leaves the host.
//
// Aborting is the whole of the enforcement. Only a denied *navigation* also fails the
// caller's operation, because that is the document the plugin asked for. A denied
// subresource (a third-party font, tag manager, analytics beacon) is blocked and
// recorded, and the page loads without it -- a real site references hosts no plugin
// author can enumerate, and failing the navigation on the first one made every login
// unreachable.
func (p *pwPage) onRoute(route playwright.Route) {
	req := route.Request()
	raw := req.URL()
	nav := req.IsNavigationRequest()
	u, err := url.Parse(raw)
	if err != nil {
		p.block(nav, raw, denyTarget("scheme", raw+" is not a parseable URL"))
		_ = route.Abort("blockedbyclient")
		return
	}
	if internalScheme(u) {
		_ = route.Continue()
		return
	}
	p.mu.Lock()
	check := p.check
	p.mu.Unlock()
	if check == nil {
		// The page fired a request outside any Goto/Get/Click, so there is no
		// allowlist to check it against. Fail closed and name what was blocked.
		p.block(nav, raw, denyTarget("host", raw+" was requested outside a navigation"))
		_ = route.Abort("blockedbyclient")
		return
	}
	if err := check(u); err != nil {
		p.block(nav, raw, err)
		_ = route.Abort("blockedbyclient")
		return
	}
	if err := p.sess.proxy.checkHost(context.Background(), u.Hostname()); err != nil {
		p.block(nav, raw, err)
		_ = route.Abort("blockedbyclient")
		return
	}
	_ = route.Continue()
}

// block records an aborted request. A navigation failure is returned to the caller;
// a subresource failure is only reported, so the caller can log what the page lost.
func (p *pwPage) block(nav bool, raw string, err error) {
	if nav {
		p.setDenied(err)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.blocked) >= maxBlockedReports {
		return
	}
	for _, b := range p.blocked {
		if b.URL == raw {
			return
		}
	}
	p.blocked = append(p.blocked, Blocked{URL: raw, Reason: deniedReason(err), Err: err.Error()})
}

// TakeBlocked returns and clears the subresources aborted since the last call.
func (p *pwPage) TakeBlocked() []Blocked {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.blocked
	p.blocked = nil
	return out
}

func (p *pwPage) onResponse(resp playwright.Response) {
	if resp == nil {
		return
	}
	req := resp.Request()
	if req == nil {
		return
	}
	switch strings.ToLower(req.ResourceType()) {
	case "xhr", "fetch":
	default:
		return
	}
	raw := req.URL()
	u, err := url.Parse(raw)
	if err != nil {
		return
	}
	p.mu.Lock()
	check := p.check
	closed := p.closed
	p.mu.Unlock()
	if closed || check == nil {
		return
	}
	if err := check(u); err != nil {
		return
	}
	body, err := resp.Body()
	if err != nil || len(body) == 0 {
		return
	}
	mime := ""
	if h := resp.Headers(); h != nil {
		mime = h["content-type"]
	}
	if !captureMIME(mime, body) {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.captured = appendCaptured(p.captured, Resource{
		URL:    raw,
		MIME:   mime,
		Body:   body,
		Status: resp.Status(),
	}, maxCapturedResponses, defaultMaxResource)
}

func (p *pwPage) Resources() []Resource {
	p.mu.Lock()
	defer p.mu.Unlock()
	return copyResources(p.captured)
}

func (p *pwPage) setDenied(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.denied == nil {
		p.denied = err
	}
}

func (p *pwPage) takeDenied() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	err := p.denied
	p.denied = nil
	return err
}

func (p *pwPage) Goto(ctx context.Context, u *url.URL, check func(*url.URL) error) (string, *url.URL, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return "", nil, ErrClosed
	}
	p.check = check
	p.denied = nil
	page := p.page
	p.mu.Unlock()

	resp, err := page.Goto(u.String(), playwright.PageGotoOptions{
		Timeout:   timeoutMS(ctx),
		WaitUntil: playwright.WaitUntilStateLoad,
	})
	if denied := p.takeDenied(); denied != nil {
		return "", nil, denied
	}
	if err != nil {
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		if p.sessClosed() {
			return "", nil, ErrClosed
		}
		return "", nil, fmt.Errorf("%w: %v", ErrEngine, err)
	}
	doc, err := page.Content()
	if err != nil {
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		return "", nil, fmt.Errorf("%w: %v", ErrEngine, err)
	}
	final := u
	if resp != nil {
		if parsed, err := url.Parse(resp.URL()); err == nil {
			final = parsed
		}
	}
	return doc, final, nil
}

func (p *pwPage) Get(ctx context.Context, u *url.URL, check func(*url.URL) error) (Resource, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return Resource{}, ErrClosed
	}
	p.check = check
	page := p.page
	p.mu.Unlock()

	current := *u
	for hops := 0; hops <= maxRedirects; hops++ {
		if ctx.Err() != nil {
			return Resource{}, ctx.Err()
		}
		if check != nil {
			if err := check(&current); err != nil {
				return Resource{}, err
			}
		}
		if err := p.sess.proxy.checkHost(ctx, current.Hostname()); err != nil {
			return Resource{}, err
		}
		resp, err := page.Request().Get(current.String(), playwright.APIRequestContextGetOptions{
			FailOnStatusCode: playwright.Bool(false),
			MaxRedirects:     playwright.Int(0),
			Timeout:          timeoutMS(ctx),
		})
		if err != nil {
			if ctx.Err() != nil {
				return Resource{}, ctx.Err()
			}
			if denied := p.takeDenied(); denied != nil {
				return Resource{}, denied
			}
			return Resource{}, fmt.Errorf("%w: %v", ErrEngine, err)
		}
		status := resp.Status()
		if status >= 300 && status < 400 {
			loc := header(resp, "location")
			_ = resp.Dispose()
			if loc == "" {
				return Resource{}, fmt.Errorf("%w: redirect missing location", ErrEngine)
			}
			next, err := current.Parse(loc)
			if err != nil {
				return Resource{}, fmt.Errorf("%w: scheme", ErrDenied)
			}
			current = *next
			continue
		}
		body, err := resp.Body()
		mime := header(resp, "content-type")
		finalURL := resp.URL()
		_ = resp.Dispose()
		if err != nil {
			return Resource{}, fmt.Errorf("%w: %v", ErrEngine, err)
		}
		if i := strings.IndexByte(mime, ';'); i >= 0 {
			mime = strings.TrimSpace(mime[:i])
		}
		if finalURL == "" {
			finalURL = current.String()
		}
		return Resource{URL: finalURL, MIME: mime, Body: body, Status: status}, nil
	}
	return Resource{}, fmt.Errorf("%w: too many redirects", ErrEngine)
}

func (p *pwPage) Content(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return "", ErrClosed
	}
	doc, err := p.page.Content()
	if err != nil {
		if p.sessClosed() {
			return "", ErrClosed
		}
		return "", err
	}
	return doc, nil
}

func (p *pwPage) Fill(ctx context.Context, selector, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return ErrClosed
	}
	page := p.page
	p.mu.Unlock()
	err := page.Fill(selector, value, playwright.PageFillOptions{Timeout: timeoutMS(ctx)})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if p.sessClosed() {
			return ErrClosed
		}
		return fmt.Errorf("%w: %v", ErrEngine, err)
	}
	return nil
}

func (p *pwPage) Click(ctx context.Context, selector string, check func(*url.URL) error) (string, *url.URL, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return "", nil, ErrClosed
	}
	p.check = check
	p.denied = nil
	page := p.page
	p.mu.Unlock()

	err := page.Click(selector, playwright.PageClickOptions{Timeout: timeoutMS(ctx)})
	if denied := p.takeDenied(); denied != nil {
		return "", nil, denied
	}
	if err != nil {
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		if p.sessClosed() {
			return "", nil, ErrClosed
		}
		return "", nil, fmt.Errorf("%w: %v", ErrEngine, err)
	}
	doc, err := page.Content()
	if err != nil {
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		return "", nil, fmt.Errorf("%w: %v", ErrEngine, err)
	}
	final, err := url.Parse(page.URL())
	if err != nil {
		return doc, nil, nil
	}
	return doc, final, nil
}

func (p *pwPage) Close(context.Context) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	page := p.page
	p.mu.Unlock()
	if page == nil {
		return nil
	}
	return page.Close()
}

func (p *pwPage) sessClosed() bool {
	p.sess.mu.Lock()
	defer p.sess.mu.Unlock()
	return p.sess.closed
}

func internalScheme(u *url.URL) bool {
	switch strings.ToLower(u.Scheme) {
	case "about", "blob", "data", "chrome", "chrome-untrusted", "chrome-extension", "devtools", "inspector":
		return true
	}
	return false
}

func timeoutMS(ctx context.Context) *float64 {
	dl, ok := ctx.Deadline()
	if !ok {
		return nil
	}
	ms := time.Until(dl).Seconds() * 1000
	if ms < 1 {
		ms = 1
	}
	return playwright.Float(ms)
}

func header(resp playwright.APIResponse, name string) string {
	for k, v := range resp.Headers() {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

var _ io.Closer = (*Playwright)(nil)
var _ Engine = (*Playwright)(nil)

// GatesRequests reports that route interception checks every request this page makes.
func (p *pwPage) GatesRequests() bool { return true }
