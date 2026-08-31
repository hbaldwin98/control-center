package web

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hbaldwin98/control-center/internal/core/ai"
	"github.com/hbaldwin98/control-center/internal/core/credentials"
	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/policy"
)

func newCredsHarness(t *testing.T, oauth map[string]credentials.OAuthProvider) (*harness, *credentials.Store) {
	t.Helper()
	h := newHarness(t)
	bus, err := events.New(h.store, h.store, events.Options{})
	if err != nil {
		t.Fatalf("events.New: %v", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	opts := credentials.Options{
		Keys: map[int][]byte{1: key}, Active: 1,
		AllowedRedirectURIs: []string{"http://127.0.0.1:8080/api/admin/credentials/oauth/callback"},
		OAuth:               oauth,
		HTTPClient:          http.DefaultClient,
	}
	creds, err := credentials.New(h.store, h.store, bus, opts)
	if err != nil {
		t.Fatalf("credentials.New: %v", err)
	}
	h.server.deps.Events = bus
	h.server.deps.Credentials = creds
	return h, creds
}

func (h *harness) reauth() {
	h.t.Helper()
	if rec := h.do(http.MethodPost, "/api/auth/reauth", map[string]string{"password": testPassword}); rec.Code != http.StatusOK {
		h.t.Fatalf("reauth: %d %s", rec.Code, rec.Body)
	}
}

func TestCredentialAdminRequiresAuthAndReauth(t *testing.T) {
	h, _ := newCredsHarness(t, nil)
	if rec := h.do(http.MethodGet, "/api/admin/credentials", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
	h.bootstrapAdmin()
	if rec := h.do(http.MethodPost, "/api/admin/credentials",
		map[string]string{"id": "google-api", "provider": "google", "secret": "sk-secret"}); rec.Code != http.StatusForbidden {
		t.Fatalf("create without reauth: got %d %s", rec.Code, rec.Body)
	}
	var env errorEnvelope
	if err := json.Unmarshal(h.do(http.MethodPost, "/api/admin/credentials",
		map[string]string{"id": "google-api", "provider": "google", "secret": "sk-secret"}).Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Error.Code != CodeReauthRequired {
		t.Fatalf("code = %q", env.Error.Code)
	}
}

func TestCredentialCreateReplaceDeleteNeverExposeSecret(t *testing.T) {
	h, creds := newCredsHarness(t, nil)
	h.bootstrapAdmin()
	h.reauth()

	const secret = "sk-never-leave-the-server"
	rec := h.do(http.MethodPost, "/api/admin/credentials",
		map[string]string{"id": "google-api", "provider": "google", "secret": secret})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Fatal("create response leaked the secret")
	}

	rec = h.do(http.MethodGet, "/api/admin/credentials", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), secret) || strings.Contains(rec.Body.String(), "secret_envelope") {
		t.Fatalf("list leaked secret material: %s", rec.Body)
	}
	var snap struct {
		Data []credentials.Credential `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Data) != 1 || snap.Data[0].ID != "google-api" || snap.Data[0].Version != 1 {
		t.Fatalf("list = %+v", snap.Data)
	}

	rec = h.do(http.MethodPut, "/api/admin/credentials/google-api", map[string]string{"secret": "sk-replaced"})
	if rec.Code != http.StatusOK {
		t.Fatalf("replace: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "sk-replaced") {
		t.Fatal("replace response leaked the secret")
	}

	rec = h.do(http.MethodPost, "/api/admin/credentials/google-api/rotate", map[string]string{"secret": "sk-rotated"})
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate: %d %s", rec.Code, rec.Body)
	}

	tok, err := creds.Token(context.Background(), "google-api")
	if err != nil || tok != "sk-rotated" {
		t.Fatalf("token = %q %v", tok, err)
	}

	if rec := h.do(http.MethodDelete, "/api/admin/credentials/google-api", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
}

func TestCredentialDeleteInUse(t *testing.T) {
	h, creds := newCredsHarness(t, nil)
	h.bootstrapAdmin()
	h.reauth()
	if rec := h.do(http.MethodPost, "/api/admin/credentials",
		map[string]string{"id": "k", "provider": "fake", "secret": "tok"}); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if err := creds.Replace(context.Background(), "ai.routes", []string{"k"}); err != nil {
		t.Fatal(err)
	}
	rec := h.do(http.MethodGet, "/api/admin/credentials/k/references", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ai.routes") {
		t.Fatalf("references: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do(http.MethodDelete, "/api/admin/credentials/k", nil); rec.Code != http.StatusConflict {
		t.Fatalf("delete in use: got %d %s", rec.Code, rec.Body)
	}
}

func TestOAuthBeginAndCallback(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok-live", "refresh_token": "ref-live", "expires_in": 3600,
		})
	}))
	t.Cleanup(ts.Close)

	h, creds := newCredsHarness(t, map[string]credentials.OAuthProvider{
		"google": {AuthURL: ts.URL + "/auth", TokenURL: ts.URL, ClientID: "cid", Scopes: []string{"openid"}},
	})
	h.bootstrapAdmin()
	h.reauth()

	rec := h.do(http.MethodPost, "/api/admin/credentials/oauth/google/begin", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("begin: %d %s", rec.Code, rec.Body)
	}
	var begin struct {
		AuthURL string `json:"authUrl"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &begin); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rec.Body.String(), "tok-live") {
		t.Fatal("begin leaked a token")
	}
	u, err := url.Parse(begin.AuthURL)
	if err != nil {
		t.Fatal(err)
	}
	state := u.Query().Get("state")
	if state == "" || u.Query().Get("code_challenge_method") != "S256" {
		t.Fatalf("auth url = %s", begin.AuthURL)
	}

	rec = h.do(http.MethodGet, "/api/admin/credentials/oauth/callback?code=auth-code&state="+url.QueryEscape(state), nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("callback: %d %s", rec.Code, rec.Body)
	}
	if loc := rec.Header().Get("Location"); loc != "/settings?oauth=ok" {
		t.Fatalf("location = %q", loc)
	}

	tok, err := creds.Token(context.Background(), "oauth-google")
	if err != nil || tok != "tok-live" {
		t.Fatalf("token = %q %v", tok, err)
	}
	list := h.do(http.MethodGet, "/api/admin/credentials", nil)
	if strings.Contains(list.Body.String(), "tok-live") || strings.Contains(list.Body.String(), "ref-live") {
		t.Fatalf("list leaked oauth tokens: %s", list.Body)
	}
}

func TestAIRoutesAndCalls(t *testing.T) {
	h, creds := newCredsHarness(t, nil)
	h.bootstrapAdmin()
	h.reauth()
	if rec := h.do(http.MethodPost, "/api/admin/credentials",
		map[string]string{"id": "fake-key", "provider": "fake", "secret": "test-token"}); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}

	ctx := context.Background()
	pol, err := policy.New(h.store, h.store, h.server.deps.Events, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := pol.Register(ctx, "hello", false); err != nil {
		t.Fatal(err)
	}
	if err := pol.Enable(ctx, "hello", "test", "setup"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte(`
routes:
  cheap-chat:
    capabilities: [chat]
    maxInputTokens: 128
    maxOutputTokens: 64
    attempts:
      - provider: fake
        model: echo
        credential: fake-key
        inputMicroUSDPerMillion: 1000000
        outputMicroUSDPerMillion: 2000000
`), 0o600); err != nil {
		t.Fatal(err)
	}
	compiled, err := ai.LoadRoutes(path)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := ai.New(h.store, h.store, h.server.deps.Events, pol, creds, ai.Options{
		Routes: compiled, Providers: []ai.Provider{ai.Fake{}}, Refs: creds,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.server.deps.Policy = pol
	h.server.deps.AI = svc

	rec := h.do(http.MethodGet, "/api/admin/ai/routes", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("routes: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "test-token") || strings.Contains(rec.Body.String(), "fake-key") {
		t.Fatalf("routes leaked credential: %s", rec.Body)
	}

	_, err = svc.Chat(ai.WithPlugin(ctx, "hello"), ai.ChatRequest{
		Model: "cheap-chat", Messages: []ai.Message{{Role: "user", Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	rec = h.do(http.MethodGet, "/api/ai/calls", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("calls: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "cheap-chat") || !strings.Contains(rec.Body.String(), "hello") {
		t.Fatalf("calls = %s", rec.Body)
	}
}
