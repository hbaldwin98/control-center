package browser

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"

	"golang.org/x/net/html"
)

const maxRedirects = 10

// Fake is an in-process engine: no Chromium, no real sockets. Tests and local
// development supply an http.Handler per DNS name; Goto is allowlist-checked then
// served from that handler.
type Fake struct {
	mu     sync.Mutex
	routes map[string]http.Handler
}

// NewFake maps lowercase DNS names to in-process handlers.
func NewFake(routes map[string]http.Handler) *Fake {
	cp := make(map[string]http.Handler, len(routes))
	for host, h := range routes {
		if h == nil {
			continue
		}
		cp[strings.ToLower(strings.TrimSuffix(host, "."))] = h
	}
	return &Fake{routes: cp}
}

// HTMLHandler serves a single HTML document for every request.
func HTMLHandler(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, body)
	})
}

func (f *Fake) NewSession(_ context.Context, _ string) (EngineSession, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &fakeSession{fake: f, jar: jar}, nil
}

type fakeSession struct {
	fake *Fake
	jar  *cookiejar.Jar
}

func (s *fakeSession) NewPage(context.Context) (EnginePage, error) {
	return &fakePage{sess: s}, nil
}

func (s *fakeSession) Close(context.Context) error { return nil }

type fakePage struct {
	sess     *fakeSession
	mu       sync.Mutex
	closed   bool
	html     string
	url      *url.URL
	captured []Resource
}

func (p *fakePage) Goto(ctx context.Context, u *url.URL, check func(*url.URL) error) (string, *url.URL, error) {
	res, err := p.fetch(ctx, u, http.MethodGet, "", nil, true, check)
	if err != nil {
		return "", nil, err
	}
	final, err := url.Parse(res.URL)
	if err != nil {
		final = u
	}
	p.mu.Lock()
	p.html = string(res.Body)
	p.url = final
	p.mu.Unlock()
	return string(res.Body), final, nil
}

func (p *fakePage) Get(ctx context.Context, u *url.URL, check func(*url.URL) error) (Resource, error) {
	return p.fetch(ctx, u, http.MethodGet, "", nil, false, check)
}

func (p *fakePage) Post(ctx context.Context, u *url.URL, form url.Values, check func(*url.URL) error) (Resource, error) {
	if form == nil {
		form = url.Values{}
	}
	return p.fetch(ctx, u, http.MethodPost, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()), false, check)
}

func (p *fakePage) Resources() []Resource {
	p.mu.Lock()
	defer p.mu.Unlock()
	return copyResources(p.captured)
}

func (p *fakePage) Content(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return "", ErrClosed
	}
	return p.html, nil
}

func (p *fakePage) Fill(ctx context.Context, selector, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrClosed
	}
	if p.html == "" {
		return fmt.Errorf("%w: no document", ErrEngine)
	}
	root, err := html.Parse(strings.NewReader(p.html))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrEngine, err)
	}
	sel, err := parseSelector(selector)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrEngine, err)
	}
	n := findIn(root, sel)
	if n == nil {
		return fmt.Errorf("%w: selector %q not found", ErrEngine, selector)
	}
	setAttr(n, "value", value)
	var buf bytes.Buffer
	if err := html.Render(&buf, root); err != nil {
		return fmt.Errorf("%w: %v", ErrEngine, err)
	}
	p.html = buf.String()
	return nil
}

func (p *fakePage) Click(ctx context.Context, selector string, check func(*url.URL) error) (string, *url.URL, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return "", nil, ErrClosed
	}
	doc := p.html
	cur := p.url
	p.mu.Unlock()
	if doc == "" {
		return "", nil, fmt.Errorf("%w: no document", ErrEngine)
	}
	root, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		return "", nil, fmt.Errorf("%w: %v", ErrEngine, err)
	}
	sel, err := parseSelector(selector)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %v", ErrEngine, err)
	}
	n := findIn(root, sel)
	if n == nil {
		return "", nil, fmt.Errorf("%w: selector %q not found", ErrEngine, selector)
	}
	if n.Data == "a" {
		href := attr(n, "href")
		if href == "" {
			return doc, cur, nil
		}
		if cur == nil {
			return "", nil, fmt.Errorf("%w: no document url", ErrEngine)
		}
		next, err := cur.Parse(href)
		if err != nil {
			return "", nil, fmt.Errorf("%w: scheme", ErrDenied)
		}
		return p.Goto(ctx, next, check)
	}
	form := enclosingForm(n)
	if form == nil {
		return doc, cur, nil
	}
	if cur == nil {
		return "", nil, fmt.Errorf("%w: no document url", ErrEngine)
	}
	action := attr(form, "action")
	target := *cur
	if action != "" {
		parsed, err := cur.Parse(action)
		if err != nil {
			return "", nil, fmt.Errorf("%w: scheme", ErrDenied)
		}
		target = *parsed
	}
	method := strings.ToUpper(strings.TrimSpace(attr(form, "method")))
	if method == "" {
		method = http.MethodGet
	}
	values := formValues(form)
	if method == http.MethodGet {
		q := target.Query()
		for k, v := range values {
			q.Set(k, v)
		}
		target.RawQuery = q.Encode()
		return p.Goto(ctx, &target, check)
	}
	body := url.Values{}
	for k, v := range values {
		body.Set(k, v)
	}
	res, err := p.fetch(ctx, &target, method, "application/x-www-form-urlencoded", strings.NewReader(body.Encode()), true, check)
	if err != nil {
		return "", nil, err
	}
	final, err := url.Parse(res.URL)
	if err != nil {
		final = &target
	}
	p.mu.Lock()
	p.html = string(res.Body)
	p.url = final
	p.mu.Unlock()
	return string(res.Body), final, nil
}

func (p *fakePage) Close(context.Context) error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	return nil
}

func findIn(n *html.Node, s selector) *html.Node {
	if n == nil {
		return nil
	}
	if s.match(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findIn(c, s); found != nil {
			return found
		}
	}
	return nil
}

func setAttr(n *html.Node, key, val string) {
	for i, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: val})
}

func enclosingForm(n *html.Node) *html.Node {
	for p := n; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && p.Data == "form" {
			return p
		}
	}
	return nil
}

func formValues(form *html.Node) map[string]string {
	out := map[string]string{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "input", "textarea", "select":
				name := attr(n, "name")
				if name == "" {
					name = attr(n, "formcontrolname")
				}
				if name != "" {
					out[name] = attr(n, "value")
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(form)
	return out
}

func (p *fakePage) fetch(ctx context.Context, start *url.URL, method, contentType string, body io.Reader, follow bool, check func(*url.URL) error) (Resource, error) {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return Resource{}, ErrClosed
	}

	current := *start
	for hops := 0; hops <= maxRedirects; hops++ {
		if ctx.Err() != nil {
			return Resource{}, ctx.Err()
		}
		if check != nil {
			if err := check(&current); err != nil {
				return Resource{}, err
			}
		}
		host := strings.ToLower(current.Hostname())
		p.sess.fake.mu.Lock()
		h := p.sess.fake.routes[host]
		p.sess.fake.mu.Unlock()
		if h == nil {
			return Resource{}, fmt.Errorf("%w: host", ErrDenied)
		}

		reqMethod := method
		var reqBody io.Reader = body
		if hops > 0 {
			reqMethod = http.MethodGet
			reqBody = nil
		}
		if reqMethod == "" {
			reqMethod = http.MethodGet
		}
		req, err := http.NewRequestWithContext(ctx, reqMethod, current.String(), reqBody)
		if err != nil {
			return Resource{}, fmt.Errorf("%w: %v", ErrEngine, err)
		}
		if contentType != "" && reqBody != nil {
			req.Header.Set("Content-Type", contentType)
		}
		for _, c := range p.sess.jar.Cookies(&current) {
			req.AddCookie(c)
		}

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		p.mu.Lock()
		closed := p.closed
		p.mu.Unlock()
		if closed || ctx.Err() != nil {
			return Resource{}, ErrClosed
		}
		res := rec.Result()
		p.sess.jar.SetCookies(&current, res.Cookies())

		if follow && res.StatusCode >= 300 && res.StatusCode < 400 {
			loc := res.Header.Get("Location")
			_ = res.Body.Close()
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

		raw, err := readCapped(res.Body, 10<<20+1)
		_ = res.Body.Close()
		if err != nil {
			return Resource{}, err
		}
		mime := res.Header.Get("Content-Type")
		if i := strings.IndexByte(mime, ';'); i >= 0 {
			mime = strings.TrimSpace(mime[:i])
		}
		out := Resource{URL: current.String(), MIME: mime, Body: raw, Status: res.StatusCode}
		if captureMIME(mime, raw) {
			p.mu.Lock()
			p.captured = appendCaptured(p.captured, out, maxCapturedResponses, 10<<20)
			p.mu.Unlock()
		}
		return out, nil
	}
	return Resource{}, fmt.Errorf("%w: too many redirects", ErrEngine)
}

func readCapped(r io.Reader, capn int) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, int64(capn)))
	if err != nil {
		return nil, err
	}
	return b, nil
}
