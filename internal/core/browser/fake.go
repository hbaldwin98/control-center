package browser

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
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
	sess   *fakeSession
	mu     sync.Mutex
	closed bool
}

func (p *fakePage) Goto(ctx context.Context, u *url.URL, check func(*url.URL) error) (string, *url.URL, error) {
	res, err := p.fetch(ctx, u, true, check)
	if err != nil {
		return "", nil, err
	}
	final, err := url.Parse(res.URL)
	if err != nil {
		return string(res.Body), u, nil
	}
	return string(res.Body), final, nil
}

func (p *fakePage) Get(ctx context.Context, u *url.URL, check func(*url.URL) error) (Resource, error) {
	return p.fetch(ctx, u, false, check)
}

func (p *fakePage) Close(context.Context) error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	return nil
}

func (p *fakePage) fetch(ctx context.Context, start *url.URL, follow bool, check func(*url.URL) error) (Resource, error) {
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

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, current.String(), nil)
		if err != nil {
			return Resource{}, fmt.Errorf("%w: %v", ErrEngine, err)
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

		body, err := readCapped(res.Body, 10<<20+1)
		_ = res.Body.Close()
		if err != nil {
			return Resource{}, err
		}
		mime := res.Header.Get("Content-Type")
		if i := strings.IndexByte(mime, ';'); i >= 0 {
			mime = strings.TrimSpace(mime[:i])
		}
		return Resource{URL: current.String(), MIME: mime, Body: body, Status: res.StatusCode}, nil
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
