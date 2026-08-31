package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hbaldwin98/control-center/internal/config"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

// harness is a server plus the client state a browser would hold.
type harness struct {
	t      *testing.T
	server *Server
	http   http.Handler
	store  *storage.Store

	cookie string
	csrf   string
	origin string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	store, err := storage.Open(ctx, storage.Options{Path: filepath.Join(t.TempDir(), "web.db")})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cfg := config.Default()
	cfg.Server.Addr = "127.0.0.1:8080"

	srv, err := New(ctx, store, Deps{DB: store, Config: cfg})
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	return &harness{
		t:      t,
		server: srv,
		http:   srv.Handler(),
		store:  store,
		origin: "http://127.0.0.1:8080",
	}
}

// bootstrapToken reads the one-time token the way an operator reads it from the log: it is
// only stored hashed, so the test regenerates it deterministically instead.
func (h *harness) issueToken() string {
	h.t.Helper()
	token, err := h.server.auth.ensureBootstrapToken(context.Background())
	if err != nil {
		h.t.Fatalf("ensureBootstrapToken: %v", err)
	}
	return token
}

func (h *harness) do(method, path string, body any) *httptest.ResponseRecorder {
	h.t.Helper()
	var r io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		r = bytes.NewReader(buf)
	}
	req := httptest.NewRequest(method, path, r)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Host = "127.0.0.1:8080"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if h.origin != "" {
		req.Header.Set("Origin", h.origin)
	}
	if h.cookie != "" {
		req.Header.Set("Cookie", sessionCookie+"="+h.cookie)
	}
	if h.csrf != "" {
		req.Header.Set(csrfHeader, h.csrf)
	}

	rec := httptest.NewRecorder()
	h.http.ServeHTTP(rec, req)
	h.captureSession(rec)
	return rec
}

func (h *harness) captureSession(rec *httptest.ResponseRecorder) {
	for _, c := range rec.Result().Cookies() {
		if c.Name != sessionCookie {
			continue
		}
		if c.MaxAge < 0 {
			h.cookie = ""
		} else {
			h.cookie = c.Value
		}
	}
	var res struct {
		CSRFToken string `json:"csrfToken"`
		Data      struct {
			CSRFToken string `json:"csrfToken"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err == nil {
		if res.CSRFToken != "" {
			h.csrf = res.CSRFToken
		} else if res.Data.CSRFToken != "" {
			h.csrf = res.Data.CSRFToken
		}
	}
}

func (h *harness) status() authStatus {
	h.t.Helper()
	rec := h.do(http.MethodGet, "/api/auth/status", nil)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("status: %d %s", rec.Code, rec.Body)
	}
	var st authStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		h.t.Fatal(err)
	}
	return st
}

func (h *harness) bootstrapAdmin() {
	h.t.Helper()
	token := h.issueToken()
	rec := h.do(http.MethodPost, "/api/auth/bootstrap",
		map[string]string{"token": token, "password": testPassword})
	if rec.Code != http.StatusOK {
		h.t.Fatalf("bootstrap: %d %s", rec.Code, rec.Body)
	}
}

const testPassword = "correct horse battery staple"

func TestBootstrapPasswordCreatesAdmin(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, storage.Options{Path: filepath.Join(t.TempDir(), "web.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := config.Default()
	cfg.Server.Addr = "127.0.0.1:8080"
	srv, err := New(ctx, store, Deps{DB: store, Config: cfg, BootstrapPassword: testPassword})
	if err != nil {
		t.Fatal(err)
	}
	exists, err := srv.auth.adminExists(ctx)
	if err != nil || !exists {
		t.Fatalf("admin exists=%v err=%v", exists, err)
	}
	if rec := httptest.NewRecorder(); true {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(
			`{"password":"correct horse battery staple"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://127.0.0.1:8080")
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("login: %d %s", rec.Code, rec.Body)
		}
	}
}

func TestFirstRunBootstrapAndLogin(t *testing.T) {
	h := newHarness(t)

	if st := h.status(); !st.BootstrapRequired || !st.BootstrapAvailable || st.Authenticated {
		t.Fatalf("unexpected initial status: %+v", st)
	}

	token := h.issueToken()

	if rec := h.do(http.MethodPost, "/api/auth/bootstrap",
		map[string]string{"token": "wrong", "password": testPassword}); rec.Code != http.StatusForbidden {
		t.Fatalf("bad token: got %d, want 403", rec.Code)
	}
	if rec := h.do(http.MethodPost, "/api/auth/bootstrap",
		map[string]string{"token": token, "password": "short"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("short password: got %d, want 400", rec.Code)
	}

	rec := h.do(http.MethodPost, "/api/auth/bootstrap",
		map[string]string{"token": token, "password": testPassword})
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap: %d %s", rec.Code, rec.Body)
	}
	if h.cookie == "" || h.csrf == "" {
		t.Fatal("bootstrap did not start a session")
	}

	// The token is single use, and bootstrap is closed once an administrator exists.
	if rec := h.do(http.MethodPost, "/api/auth/bootstrap",
		map[string]string{"token": token, "password": testPassword}); rec.Code != http.StatusForbidden {
		t.Fatalf("token reuse: got %d, want 403", rec.Code)
	}

	if st := h.status(); st.BootstrapRequired || !st.Authenticated {
		t.Fatalf("unexpected post-bootstrap status: %+v", st)
	}

	// The shell bootstrap snapshot carries the envelope the client depends on.
	rec = h.do(http.MethodGet, "/api/bootstrap", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("shell bootstrap: %d %s", rec.Code, rec.Body)
	}
	var snap struct {
		Data        shellBootstrap `json:"data"`
		AsOfEventID string         `json:"asOfEventId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.AsOfEventID == "" {
		t.Fatal("snapshot is missing asOfEventId")
	}
	if snap.Data.CSRFToken == "" {
		t.Fatal("snapshot is missing the synchronizer token")
	}

	// Logout invalidates the session server side.
	if rec := h.do(http.MethodPost, "/api/auth/logout", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("logout: %d %s", rec.Code, rec.Body)
	}
	if h.cookie != "" {
		t.Fatal("logout did not clear the cookie")
	}
	if rec := h.do(http.MethodGet, "/api/bootstrap", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("post-logout bootstrap: got %d, want 401", rec.Code)
	}

	// Login with the wrong password fails; the right one starts a fresh session.
	h.csrf = ""
	if rec := h.do(http.MethodPost, "/api/auth/login",
		map[string]string{"password": "nope"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad login: got %d, want 401", rec.Code)
	}
	if rec := h.do(http.MethodPost, "/api/auth/login",
		map[string]string{"password": testPassword}); rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	if h.cookie == "" {
		t.Fatal("login did not start a session")
	}
}

func TestBootstrapRejectsNonLoopbackPeer(t *testing.T) {
	h := newHarness(t)
	token := h.issueToken()

	req := httptest.NewRequest(http.MethodPost, "/api/auth/bootstrap",
		strings.NewReader(`{"token":"`+token+`","password":"`+testPassword+`"}`))
	req.RemoteAddr = "203.0.113.7:1234"
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Origin", h.origin)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	h.http.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", rec.Code)
	}
}

func TestMutationRequiresCSRFAndOrigin(t *testing.T) {
	h := newHarness(t)
	token := h.issueToken()
	if rec := h.do(http.MethodPost, "/api/auth/bootstrap",
		map[string]string{"token": token, "password": testPassword}); rec.Code != http.StatusOK {
		t.Fatalf("bootstrap: %d %s", rec.Code, rec.Body)
	}

	good := h.csrf
	h.csrf = "not-the-token"
	if rec := h.do(http.MethodPost, "/api/auth/logout", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("bad csrf: got %d, want 403", rec.Code)
	}

	h.csrf = good
	h.origin = "https://evil.example"
	if rec := h.do(http.MethodPost, "/api/auth/logout", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("bad origin: got %d, want 403", rec.Code)
	}

	h.origin = ""
	if rec := h.do(http.MethodPost, "/api/auth/logout", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("missing origin: got %d, want 403", rec.Code)
	}

	// A read is unaffected by either check.
	h.origin = "http://localhost:8080"
	if rec := h.do(http.MethodGet, "/api/bootstrap", nil); rec.Code != http.StatusOK {
		t.Fatalf("read with alias origin: %d %s", rec.Code, rec.Body)
	}
}

func TestSessionCookieAttributes(t *testing.T) {
	h := newHarness(t)
	token := h.issueToken()
	rec := h.do(http.MethodPost, "/api/auth/bootstrap",
		map[string]string{"token": token, "password": testPassword})

	var found *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			found = c
		}
	}
	if found == nil {
		t.Fatal("no session cookie")
	}
	if !strings.HasPrefix(found.Name, "__Host-") {
		t.Errorf("cookie name %q lacks the __Host- prefix", found.Name)
	}
	if !found.HttpOnly || !found.Secure {
		t.Errorf("cookie must be HttpOnly and Secure: %+v", found)
	}
	if found.Path != "/" || found.Domain != "" {
		t.Errorf("__Host- requires Path=/ and no Domain: path=%q domain=%q", found.Path, found.Domain)
	}
	if found.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", found.SameSite)
	}
}

func TestIdleSessionExpires(t *testing.T) {
	h := newHarness(t)
	token := h.issueToken()
	if rec := h.do(http.MethodPost, "/api/auth/bootstrap",
		map[string]string{"token": token, "password": testPassword}); rec.Code != http.StatusOK {
		t.Fatalf("bootstrap: %d %s", rec.Code, rec.Body)
	}
	cookie := h.cookie

	// A lookup with a zero idle allowance always exceeds the policy.
	if _, err := h.server.auth.lookup(context.Background(), cookie, -time.Second); err == nil {
		t.Fatal("expected the idle policy to reject the session")
	}
	// ...and the session is gone afterwards.
	if _, err := h.server.auth.lookup(context.Background(), cookie, time.Hour); err == nil {
		t.Fatal("an idle-expired session must be deleted")
	}
}

func TestReauthMarksSessionFresh(t *testing.T) {
	h := newHarness(t)
	token := h.issueToken()
	if rec := h.do(http.MethodPost, "/api/auth/bootstrap",
		map[string]string{"token": token, "password": testPassword}); rec.Code != http.StatusOK {
		t.Fatalf("bootstrap: %d %s", rec.Code, rec.Body)
	}

	if rec := h.do(http.MethodPost, "/api/auth/reauth",
		map[string]string{"password": "wrong"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad reauth: got %d, want 401", rec.Code)
	}
	if rec := h.do(http.MethodPost, "/api/auth/reauth",
		map[string]string{"password": testPassword}); rec.Code != http.StatusOK {
		t.Fatalf("reauth: %d %s", rec.Code, rec.Body)
	}

	sess, err := h.server.auth.lookup(context.Background(), h.cookie, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !sess.reauthFresh(time.Now().UTC(), 5*time.Minute) {
		t.Fatal("session should be reauthenticated")
	}
	if sess.reauthFresh(time.Now().UTC().Add(10*time.Minute), 5*time.Minute) {
		t.Fatal("reauthentication should expire with the window")
	}
}

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := hashPassword(testPassword, defaultArgon2id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("unexpected encoding: %s", hash)
	}
	if !verifyPassword(testPassword, hash) {
		t.Fatal("correct password rejected")
	}
	if verifyPassword(testPassword+"x", hash) {
		t.Fatal("wrong password accepted")
	}
	if verifyPassword(testPassword, "garbage") {
		t.Fatal("garbage hash accepted")
	}
}

func TestUnknownAPIPathIs404JSON(t *testing.T) {
	h := newHarness(t)
	rec := h.do(http.MethodGet, "/api/nope", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rec.Code)
	}
	var body errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != CodeNotFound {
		t.Fatalf("code = %q, want %q", body.Error.Code, CodeNotFound)
	}
}

func TestSPAFallbackServesShell(t *testing.T) {
	h := newHarness(t)
	rec := h.do(http.MethodGet, "/plugins", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content type = %q", ct)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); csp == "" {
		t.Fatal("missing CSP header")
	}
}
