package browser

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
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
	bus *events.Log
}

func newHarness(t *testing.T, routes map[string]http.Handler) *harness {
	t.Helper()
	return newHarnessWithEngine(t, NewFake(routes))
}

func newHarnessWithEngine(t *testing.T, eng Engine) *harness {
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
	if err := pol.Register(ctx, "hello", false); err != nil {
		t.Fatal(err)
	}
	if err := pol.Enable(ctx, "hello", "test", "go"); err != nil {
		t.Fatal(err)
	}
	svc, err := New(bus, pol, Options{Engine: eng})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return &harness{t: t, ctx: ctx, svc: svc, pol: pol, bus: bus}
}

func (h *harness) ctxHello() context.Context {
	return WithPlugin(h.ctx, "hello")
}

func TestOpenRequiresAllowlistAndPlugin(t *testing.T) {
	h := newHarness(t, map[string]http.Handler{"hello.test": HTMLHandler(`<article class="lot">x</article>`)})
	if _, err := h.svc.Open(h.ctx, OpenOptions{AllowedHosts: []string{"hello.test"}}); !errors.Is(err, ErrNoPlugin) {
		t.Fatalf("no plugin: %v", err)
	}
	if _, err := h.svc.Open(h.ctxHello(), OpenOptions{}); !errors.Is(err, ErrInvalidAllowlist) {
		t.Fatalf("empty allowlist: %v", err)
	}
	if _, err := h.svc.Open(h.ctxHello(), OpenOptions{AllowedHosts: []string{"127.0.0.1"}}); !errors.Is(err, ErrInvalidAllowlist) {
		t.Fatalf("ip allowlist: %v", err)
	}
	if _, err := h.svc.Open(h.ctxHello(), OpenOptions{AllowedHosts: []string{"hello.test:443"}}); !errors.Is(err, ErrInvalidAllowlist) {
		t.Fatalf("port allowlist: %v", err)
	}
}

func TestGotoAllowlistAndFetch(t *testing.T) {
	h := newHarness(t, map[string]http.Handler{
		"hello.test": HTMLHandler(`<!doctype html><article class="lot">hello</article>`),
	})
	sess, err := h.svc.Open(h.ctxHello(), OpenOptions{AllowedHosts: []string{"hello.test"}})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close(h.ctxHello())
	page, err := sess.NewPage(h.ctxHello())
	if err != nil {
		t.Fatal(err)
	}
	defer page.Close(h.ctxHello())

	if err := page.Goto(h.ctxHello(), "http://hello.test/"); !errors.Is(err, ErrDenied) {
		t.Fatalf("http: %v", err)
	}
	if err := page.Goto(h.ctxHello(), "https://evil.test/"); !errors.Is(err, ErrDenied) {
		t.Fatalf("host: %v", err)
	}
	if err := page.Goto(h.ctxHello(), "https://hello.test:8443/"); !errors.Is(err, ErrDenied) {
		t.Fatalf("port: %v", err)
	}
	if err := page.Goto(h.ctxHello(), "https://user@hello.test/"); !errors.Is(err, ErrDenied) {
		t.Fatalf("userinfo: %v", err)
	}
	if err := page.Goto(h.ctxHello(), "https://127.0.0.1/"); !errors.Is(err, ErrDenied) {
		t.Fatalf("ip: %v", err)
	}

	if err := page.Goto(h.ctxHello(), "https://hello.test/"); err != nil {
		t.Fatal(err)
	}
	if err := page.WaitFor(h.ctxHello(), "article.lot", time.Second); err != nil {
		t.Fatal(err)
	}
	html, err := page.Content(h.ctxHello())
	if err != nil || !strings.Contains(html, "hello") {
		t.Fatalf("content %q %v", html, err)
	}
}

func TestRedirectOffAllowlistIsDenied(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.test/x", http.StatusFound)
	})
	h := newHarness(t, map[string]http.Handler{"hello.test": mux})
	sess, err := h.svc.Open(h.ctxHello(), OpenOptions{AllowedHosts: []string{"hello.test"}})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close(h.ctxHello())
	page, err := sess.NewPage(h.ctxHello())
	if err != nil {
		t.Fatal(err)
	}
	if err := page.Goto(h.ctxHello(), "https://hello.test/"); !errors.Is(err, ErrDenied) {
		t.Fatalf("redirect: %v", err)
	}
}

func TestSubresourceOffAllowlistIsDenied(t *testing.T) {
	h := newHarness(t, map[string]http.Handler{
		"hello.test": HTMLHandler(`<html><img src="https://evil.test/x.png"></html>`),
	})
	sess, err := h.svc.Open(h.ctxHello(), OpenOptions{AllowedHosts: []string{"hello.test"}})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close(h.ctxHello())
	page, err := sess.NewPage(h.ctxHello())
	if err != nil {
		t.Fatal(err)
	}
	if err := page.Goto(h.ctxHello(), "https://hello.test/"); !errors.Is(err, ErrDenied) {
		t.Fatalf("subresource: %v", err)
	}
}

func TestInlineDataSubresourceIsAllowed(t *testing.T) {
	h := newHarness(t, map[string]http.Handler{
		"hello.test": HTMLHandler(`<html><img src="data:image/gif;base64,R0lGODlhAQABAAAAACw="></html>`),
	})
	sess, err := h.svc.Open(h.ctxHello(), OpenOptions{AllowedHosts: []string{"hello.test"}})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close(h.ctxHello())
	page, err := sess.NewPage(h.ctxHello())
	if err != nil {
		t.Fatal(err)
	}
	if err := page.Goto(h.ctxHello(), "https://hello.test/"); err != nil {
		t.Fatal(err)
	}
}

func TestSessionLimit(t *testing.T) {
	h := newHarness(t, map[string]http.Handler{"hello.test": HTMLHandler(`<p>x</p>`)})
	ctx := h.ctxHello()
	sess, err := h.svc.Open(ctx, OpenOptions{AllowedHosts: []string{"hello.test"}})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close(ctx)
	if _, err := h.svc.Open(ctx, OpenOptions{AllowedHosts: []string{"hello.test"}}); !errors.Is(err, ErrLimit) {
		t.Fatalf("second session: %v", err)
	}
}

func TestDisableClosesInFlightGoto(t *testing.T) {
	started := make(chan struct{})
	hold := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	})
	h := newHarness(t, map[string]http.Handler{"hello.test": hold})
	ctx := h.ctxHello()
	sess, err := h.svc.Open(ctx, OpenOptions{AllowedHosts: []string{"hello.test"}})
	if err != nil {
		t.Fatal(err)
	}
	page, err := sess.NewPage(ctx)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- page.Goto(ctx, "https://hello.test/")
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}
	if err := h.pol.Disable(h.ctx, "hello", "test", "kill"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("in-flight goto succeeded after disable")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight goto did not fail")
	}

	if _, err := h.svc.Open(ctx, OpenOptions{AllowedHosts: []string{"hello.test"}}); !errors.Is(err, policy.ErrPluginDisabled) {
		t.Fatalf("open after disable: %v", err)
	}
	if err := page.Goto(ctx, "https://hello.test/"); !errors.Is(err, policy.ErrPluginDisabled) && !errors.Is(err, ErrClosed) {
		t.Fatalf("goto after disable: %v", err)
	}
}

func TestGetUsesCookieJarAndLimit(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /set", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "1", Path: "/"})
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok")
	})
	mux.HandleFunc("GET /echo", func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("sid")
		if err != nil {
			http.Error(w, "no cookie", http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, c.Value)
	})
	h := newHarness(t, map[string]http.Handler{"hello.test": mux})
	h.svc.opts.MaxResourceBytes = 8
	ctx := h.ctxHello()
	sess, err := h.svc.Open(ctx, OpenOptions{AllowedHosts: []string{"hello.test"}})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close(ctx)
	page, err := sess.NewPage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := page.Get(ctx, "https://hello.test/set"); err != nil {
		t.Fatal(err)
	}
	res, err := page.Get(ctx, "https://hello.test/echo")
	if err != nil || string(res.Body) != "1" {
		t.Fatalf("cookie jar: %+v %v", res, err)
	}
}

func TestDeniedPublishesEvent(t *testing.T) {
	h := newHarness(t, map[string]http.Handler{"hello.test": HTMLHandler(`x`)})
	ctx := WithJob(h.ctxHello(), "9")
	sess, err := h.svc.Open(ctx, OpenOptions{AllowedHosts: []string{"hello.test"}})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close(ctx)
	page, err := sess.NewPage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := page.Goto(ctx, "https://evil.test/"); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		list, err := h.bus.Query(h.ctx, events.Query{Pattern: "core.browser.denied", Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(list) > 0 {
			if list[0].Source != events.SourceBrowser {
				t.Fatalf("source %q", list[0].Source)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no denied event")
}

func TestCheckResolvedIPRejectsPrivate(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.0.0.1", "192.168.1.1", "169.254.1.1", "100.64.0.1", "::1"} {
		ip := net.ParseIP(s)
		if err := checkResolvedIP(ip); !errors.Is(err, ErrDenied) {
			t.Errorf("%s: %v", s, err)
		}
	}
	if err := checkResolvedIP(net.ParseIP("8.8.8.8")); err != nil {
		t.Fatal(err)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	h := newHarness(t, map[string]http.Handler{"hello.test": HTMLHandler(`x`)})
	ctx := h.ctxHello()
	sess, err := h.svc.Open(ctx, OpenOptions{AllowedHosts: []string{"hello.test"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := sess.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
