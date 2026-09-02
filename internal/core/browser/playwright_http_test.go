package browser

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// The guards in front of the direct request paths, Get and Post.
//
// What is *not* here, deliberately: the redirect walk, the per-hop allowlist re-check,
// and the body/MIME handling inside those two methods. Reaching that code needs a real
// origin, and there is no way to stand one up locally:
//
//   - the SSRF proxy is CONNECT-only (`ssrfProxy.ServeHTTP` refuses anything else), so a
//     plain-HTTP local server is refused with 403 before the request is made; and
//   - over HTTPS the engine sets `IgnoreHttpsErrors: false`, so Chromium rejects a
//     self-signed `httptest.NewTLSServer` certificate.
//
// Both of those are correct production behaviour and neither should be relaxed to make a
// test pass. Covering the rest of Get/Post needs a CA that Chromium trusts for the test
// origin, which is real infrastructure rather than a test helper; until then those
// branches stay uncovered and their CRAP stays high, which is an honest reading.
//
// The three checks below run before any socket is opened, so they are testable as they
// stand -- and they are the ones that matter most: a page that keeps serving after close,
// or that ignores the caller's check, is a containment failure rather than a bug.

// publicIP is outside every range checkResolvedIP rejects, so the gate lets it through.
var publicIP = net.IPv4(93, 184, 216, 34)

// serveAs points `host` at `server` for the engine's proxy and returns the engine page
// underneath the service wrapper. The wrapper's Get/Post take a URL string and supply the
// allowlist check themselves; the checks under test take it as an argument.
func serveAs(t *testing.T, host string, server *httptest.Server) (*harness, EnginePage, context.Context) {
	t.Helper()
	eng := playwrightEngine(t)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	eng.proxy.lookup = func(_ context.Context, name string) ([]net.IP, error) {
		if name == host {
			return []net.IP{publicIP}, nil
		}
		return nil, fmt.Errorf("no such host: %s", name)
	}
	eng.proxy.dial = func(ctx context.Context, ip net.IP, port string) (net.Conn, error) {
		if !ip.Equal(publicIP) {
			return nil, fmt.Errorf("unexpected dial to %s", ip)
		}
		var d net.Dialer
		return d.DialContext(ctx, "tcp", target.Host)
	}

	h := newHarnessWithEngine(t, eng)
	ctx := h.ctxHello()
	sess, err := h.svc.Open(ctx, OpenOptions{AllowedHosts: []string{host}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close(ctx) })
	p, err := sess.NewPage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close(ctx) })
	inner, ok := p.(*page)
	if !ok {
		t.Fatalf("page is %T, want *page", p)
	}
	return h, inner.engine, ctx
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// unreachable fails the test if the request ever leaves the process: every case in this
// file must refuse before that.
func unreachable(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the request reached the server; it should have been refused first")
		_, _ = w.Write([]byte("unreachable"))
	}))
}

// The caller's check is the allowlist gate. It runs before the request, so refusing in it
// must mean nothing is sent -- not that a response is discarded afterwards.
func TestPlaywrightPostHonoursTheCallersCheck(t *testing.T) {
	server := unreachable(t)
	defer server.Close()

	_, page, ctx := serveAs(t, "hello.test", server)
	deny := errors.New("refused by the caller's check")
	_, err := page.Post(ctx, mustURL(t, "http://hello.test/submit"), url.Values{"a": {"b"}}, func(*url.URL) error {
		return deny
	})
	if !errors.Is(err, deny) {
		t.Fatalf("err = %v, want the check's refusal", err)
	}
}

// A closed page must refuse rather than reach the network. The session teardown depends
// on this: closing is what makes a page stop being a capability.
func TestPlaywrightRequestsRefuseOnAClosedPage(t *testing.T) {
	server := unreachable(t)
	defer server.Close()

	_, page, ctx := serveAs(t, "hello.test", server)
	if err := page.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := page.Get(ctx, mustURL(t, "http://hello.test/"), nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("get after close = %v, want ErrClosed", err)
	}
	if _, err := page.Post(ctx, mustURL(t, "http://hello.test/"), url.Values{}, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("post after close = %v, want ErrClosed", err)
	}
}

// A cancelled context stops the walk before the first hop rather than completing it.
func TestPlaywrightGetStopsOnACancelledContext(t *testing.T) {
	server := unreachable(t)
	defer server.Close()

	_, page, ctx := serveAs(t, "hello.test", server)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := page.Get(cancelled, mustURL(t, "http://hello.test/"), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("get = %v, want context.Canceled", err)
	}
}
