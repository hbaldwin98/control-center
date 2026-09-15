package web

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecurityHeadersAlwaysPresent(t *testing.T) {
	h := newHarness(t)
	rec := h.do(http.MethodGet, "/api/auth/status", nil)
	for header, want := range map[string]string{
		"Cache-Control":          "no-store",
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if csp := rec.Header().Get("Content-Security-Policy"); csp == "" {
		t.Error("no Content-Security-Policy")
	}
}

// TestHSTSFollowsTheSchemeTheBrowserUsed pins the one judgement in these headers: the
// promise is about HTTPS, so it is made only to a client that actually arrived over it.
func TestHSTSFollowsTheSchemeTheBrowserUsed(t *testing.T) {
	h := newHarness(t)

	plain := h.do(http.MethodGet, "/api/auth/status", nil)
	if got := plain.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS sent over plain HTTP: %q", got)
	}

	// A request this server terminated TLS for.
	req := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	req.RemoteAddr = h.peer
	req.TLS = &tls.ConnectionState{}
	rec := httptest.NewRecorder()
	h.http.ServeHTTP(rec, req)
	if got := rec.Header().Get("Strict-Transport-Security"); got != hstsMaxAge {
		t.Errorf("HSTS over TLS = %q, want %q", got, hstsMaxAge)
	}
}

// TestHSTSBehindTrustedProxy covers the deployment where something else terminates the
// TLS: the connection here is plain, and only the proxy can say what the browser used.
func TestHSTSBehindTrustedProxy(t *testing.T) {
	h := newHarness(t)

	forwarded := func(proto string) string {
		req := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
		req.RemoteAddr = h.peer
		if proto != "" {
			req.Header.Set("X-Forwarded-Proto", proto)
		}
		rec := httptest.NewRecorder()
		h.http.ServeHTTP(rec, req)
		return rec.Header().Get("Strict-Transport-Security")
	}

	// Untrusted, so the header proves nothing and is not acted on.
	if got := forwarded("https"); got != "" {
		t.Errorf("HSTS from an untrusted X-Forwarded-Proto: %q", got)
	}

	h.server.deps.Config.Server.TrustedProxy = true
	if got := forwarded("https"); got != hstsMaxAge {
		t.Errorf("HSTS behind a trusted proxy = %q, want %q", got, hstsMaxAge)
	}
	if got := forwarded("http"); got != "" {
		t.Errorf("HSTS sent for a plain-HTTP hop: %q", got)
	}
	if got := forwarded(""); got != "" {
		t.Errorf("HSTS sent with no forwarded scheme at all: %q", got)
	}
}

// TestHealthzIsUnauthenticatedAndSaysNothing lets a proxy or uptime check prove the
// process is answering without holding a session, and without learning anything else.
func TestHealthzIsUnauthenticatedAndSaysNothing(t *testing.T) {
	h := newHarness(t)
	h.cookie, h.csrf = "", ""

	rec := h.do(http.MethodGet, "/healthz", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz: got %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "{\"status\":\"ok\"}\n" {
		t.Errorf("healthz body = %q", got)
	}
}
