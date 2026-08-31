package credentials

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

type fixture struct {
	t     *testing.T
	s     *Store
	store *storage.Store
	bus   *events.Log
	keys  map[int][]byte
}

func newFixture(t *testing.T, oauth map[string]OAuthProvider, redirects []string) *fixture {
	t.Helper()
	ctx := context.Background()
	st, err := storage.Open(ctx, storage.Options{Path: filepath.Join(t.TempDir(), "c.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bus, err := events.New(st, st, events.Options{})
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	keys := map[int][]byte{1: key}
	s, err := New(st, st, bus, Options{
		Keys: keys, Active: 1,
		OAuth: oauth, AllowedRedirectURIs: redirects,
		HTTPClient: http.DefaultClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, s: s, store: st, bus: bus, keys: keys}
}

func adminCtx() context.Context { return WithActor(context.Background(), "admin") }

func TestCreateAPIKeyNeverReturnsSecret(t *testing.T) {
	f := newFixture(t, nil, nil)
	got, err := f.s.CreateAPIKey(adminCtx(), APIKeyInput{
		ID: "google-api", Provider: "google", Secret: SecretInput{Value: "sk-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "sk-secret") {
		t.Fatalf("admin view leaked the secret: %s", raw)
	}
	listed, err := f.s.List(context.Background())
	if err != nil || len(listed) != 1 || listed[0].ID != "google-api" || listed[0].Version != 1 {
		t.Fatalf("list = %+v err=%v", listed, err)
	}
	tok, err := f.s.Token(context.Background(), "google-api")
	if err != nil || tok != "sk-secret" {
		t.Fatalf("token = %q err=%v", tok, err)
	}
}

func TestReplaceAndRotateAPIKey(t *testing.T) {
	f := newFixture(t, nil, nil)
	ctx := adminCtx()
	if _, err := f.s.CreateAPIKey(ctx, APIKeyInput{ID: "k", Provider: "openai", Secret: SecretInput{Value: "old"}}); err != nil {
		t.Fatal(err)
	}
	got, err := f.s.ReplaceAPIKey(ctx, "k", SecretInput{Value: "new"})
	if err != nil || got.Version != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	tok, _ := f.s.Token(ctx, "k")
	if tok != "new" {
		t.Fatalf("token = %q", tok)
	}
	got, err = f.s.RotateAPIKey(ctx, "k", SecretInput{Value: "newer"})
	if err != nil || got.Version != 3 {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := f.s.CreateAPIKey(context.Background(), APIKeyInput{ID: "x", Provider: "openai", Secret: SecretInput{Value: "a"}}); !errors.Is(err, ErrNoActor) {
		t.Fatalf("no actor: %v", err)
	}
}

func TestDeleteRejectedWhileReferenced(t *testing.T) {
	f := newFixture(t, nil, nil)
	ctx := adminCtx()
	if _, err := f.s.CreateAPIKey(ctx, APIKeyInput{ID: "k", Provider: "openai", Secret: SecretInput{Value: "a"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Replace(ctx, "ai.routes", []string{"k"}); err != nil {
		t.Fatal(err)
	}
	refs, err := f.s.References(ctx, "k")
	if err != nil || len(refs) != 1 || refs[0] != "ai.routes" {
		t.Fatalf("refs = %v %v", refs, err)
	}
	if err := f.s.Delete(ctx, "k"); !errors.Is(err, ErrCredentialInUse) {
		t.Fatalf("delete: %v", err)
	}
	if err := f.s.Replace(ctx, "ai.routes", nil); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Delete(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Token(ctx, "k"); !errors.Is(err, ErrUnknownCredential) {
		t.Fatalf("token after delete: %v", err)
	}
}

func TestOAuthRoundTripAndConsumeOnce(t *testing.T) {
	var seenVerifier string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		seenVerifier = r.Form.Get("code_verifier")
		if r.Form.Get("code") != "auth-code" {
			http.Error(w, "bad code", 400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok-1", "refresh_token": "ref-1", "expires_in": 3600, "token_type": "Bearer",
		})
	}))
	t.Cleanup(ts.Close)

	redirect := "http://127.0.0.1:8080/api/admin/credentials/oauth/callback"
	f := newFixture(t, map[string]OAuthProvider{
		"google": {AuthURL: ts.URL + "/auth", TokenURL: ts.URL, ClientID: "cid", ClientSecret: "csec", Scopes: []string{"openid"}},
	}, []string{redirect})

	ctx := adminCtx()
	authURL, state, err := f.s.BeginOAuth(ctx, OAuthStart{Provider: "google", SessionID: "sess-1", RedirectURI: redirect})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("code_challenge_method") != "S256" || u.Query().Get("state") != state {
		t.Fatalf("auth url = %s", authURL)
	}

	got, err := f.s.CompleteOAuth(ctx, OAuthCallback{
		Provider: "google", SessionID: "sess-1", RedirectURI: redirect, State: state, Code: "auth-code",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "oauth-google" || got.Kind != KindOAuth || seenVerifier == "" {
		t.Fatalf("%+v verifier=%q", got, seenVerifier)
	}
	tok, err := f.s.Token(ctx, got.ID)
	if err != nil || tok != "tok-1" {
		t.Fatalf("token = %q %v", tok, err)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "tok-1") || strings.Contains(string(raw), "ref-1") {
		t.Fatalf("oauth admin view leaked tokens: %s", raw)
	}

	if _, err := f.s.CompleteOAuth(ctx, OAuthCallback{
		Provider: "google", SessionID: "sess-1", RedirectURI: redirect, State: state, Code: "auth-code",
	}); !errors.Is(err, ErrOAuthState) {
		t.Fatalf("replay: %v", err)
	}
}

func TestOAuthCompleteInfersProviderFromState(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-2", "expires_in": 60})
	}))
	t.Cleanup(ts.Close)
	redirect := "http://127.0.0.1:8080/api/admin/credentials/oauth/callback"
	f := newFixture(t, map[string]OAuthProvider{
		"google": {AuthURL: ts.URL + "/auth", TokenURL: ts.URL, ClientID: "cid"},
	}, []string{redirect})
	_, state, err := f.s.BeginOAuth(adminCtx(), OAuthStart{Provider: "google", SessionID: "sess-1", RedirectURI: redirect})
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.s.CompleteOAuth(adminCtx(), OAuthCallback{
		SessionID: "sess-1", RedirectURI: redirect, State: state, Code: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "oauth-google" || got.Provider != "google" {
		t.Fatalf("%+v", got)
	}
}

func TestOAuthRejectsUnknownRedirectAndSessionMismatch(t *testing.T) {
	f := newFixture(t, map[string]OAuthProvider{
		"google": {AuthURL: "http://example/auth", TokenURL: "http://example/token", ClientID: "cid"},
	}, []string{"http://127.0.0.1:8080/callback"})
	ctx := adminCtx()
	if _, _, err := f.s.BeginOAuth(ctx, OAuthStart{
		Provider: "google", SessionID: "s", RedirectURI: "https://evil.example/cb",
	}); !errors.Is(err, ErrRedirect) {
		t.Fatalf("evil redirect: %v", err)
	}
	_, state, err := f.s.BeginOAuth(ctx, OAuthStart{
		Provider: "google", SessionID: "s1", RedirectURI: "http://127.0.0.1:8080/callback",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.CompleteOAuth(ctx, OAuthCallback{
		Provider: "google", SessionID: "other", RedirectURI: "http://127.0.0.1:8080/callback",
		State: state, Code: "x",
	}); !errors.Is(err, ErrOAuthState) {
		t.Fatalf("session mismatch: %v", err)
	}
}

func TestStartupFailsClosedOnBadKey(t *testing.T) {
	f := newFixture(t, nil, nil)
	ctx := adminCtx()
	if _, err := f.s.CreateAPIKey(ctx, APIKeyInput{ID: "k", Provider: "openai", Secret: SecretInput{Value: "a"}}); err != nil {
		t.Fatal(err)
	}
	wrong := make([]byte, 32)
	_, err := New(f.store, f.store, f.bus, Options{Keys: map[int][]byte{1: wrong}, Active: 1})
	if err == nil {
		t.Fatal("wrong key must fail closed")
	}
}

func TestParseMasterKey(t *testing.T) {
	if _, err := ParseMasterKey(""); !errors.Is(err, ErrMasterKey) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := ParseMasterKey("zzzz"); !errors.Is(err, ErrMasterKey) {
		t.Fatalf("not hex: %v", err)
	}
	if _, err := ParseMasterKey("aa"); !errors.Is(err, ErrMasterKey) {
		t.Fatalf("short: %v", err)
	}
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	got, err := ParseMasterKey(hex.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, key) {
		t.Fatalf("got %x", got)
	}
}

func TestEmptySecretRejected(t *testing.T) {
	f := newFixture(t, nil, nil)
	if _, err := f.s.CreateAPIKey(adminCtx(), APIKeyInput{ID: "k", Provider: "openai", Secret: SecretInput{Value: ""}}); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("empty secret: %v", err)
	}
}
