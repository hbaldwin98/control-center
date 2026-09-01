package credentials

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// unsignedJWT builds a token carrying the namespaced ChatGPT claims. Nothing verifies
// the signature: the account id is routing metadata, and the provider decides whether
// the bearer token is real.
func unsignedJWT(claims map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, _ := json.Marshal(claims)
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func chatGPTIDToken(accountID, plan string) string {
	return unsignedJWT(map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": accountID,
			"chatgpt_plan_type":  plan,
		},
	})
}

// subscriptionProvider mirrors how the ChatGPT login is configured: a pinned loopback
// redirect this server can never receive, and a refresh grant that takes JSON.
func subscriptionProvider(tokenURL string) map[string]OAuthProvider {
	return map[string]OAuthProvider{"codex": {
		AuthURL:     "https://auth.example/oauth/authorize",
		TokenURL:    tokenURL,
		ClientID:    "app_test",
		RedirectURI: "http://localhost:1455/auth/callback",
		Scopes:      []string{"openid", "profile", "email", "offline_access"},
		ExtraAuthParams: map[string]string{
			"id_token_add_organizations": "true",
			"codex_cli_simplified_flow":  "true",
		},
		RefreshJSON:  true,
		AccountClaim: []string{"https://api.openai.com/auth", "chatgpt_account_id"},
		PlanClaim:    []string{"https://api.openai.com/auth", "chatgpt_plan_type"},
	}}
}

func TestPinnedRedirectFlowIgnoresTheServerAllowlist(t *testing.T) {
	f := newFixture(t, subscriptionProvider("https://auth.example/oauth/token"), nil)
	authURL, state, err := f.s.BeginOAuth(adminCtx(), OAuthStart{
		Provider: "codex", SessionID: "sess-1",
		// The caller's own callback is irrelevant for a pinned provider.
		RedirectURI: "https://control.example/api/admin/credentials/oauth/callback",
	})
	if err != nil {
		t.Fatal(err)
	}
	if state == "" {
		t.Fatal("no state was issued")
	}
	if !strings.Contains(authURL, "redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback") {
		t.Fatalf("auth url did not pin the provider's redirect: %s", authURL)
	}
	if !strings.Contains(authURL, "code_challenge_method=S256") {
		t.Fatalf("PKCE challenge missing: %s", authURL)
	}
	for _, want := range []string{"id_token_add_organizations=true", "codex_cli_simplified_flow=true"} {
		if !strings.Contains(authURL, want) {
			t.Fatalf("auth url is missing %q: %s", want, authURL)
		}
	}
}

func TestManualCallbackCompletesAndRecordsTheAccount(t *testing.T) {
	var gotForm string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotForm = string(body)
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("code exchange content type = %q, want form encoding", ct)
		}
		_, _ = w.Write([]byte(`{"access_token":"at","refresh_token":"rt","expires_in":3600,"id_token":"` +
			chatGPTIDToken("acct-9", "pro") + `"}`))
	}))
	defer srv.Close()

	f := newFixture(t, subscriptionProvider(srv.URL), nil)
	_, state, err := f.s.BeginOAuth(adminCtx(), OAuthStart{Provider: "codex", SessionID: "sess-1"})
	if err != nil {
		t.Fatal(err)
	}

	cred, err := f.s.CompleteOAuthManual(adminCtx(), OAuthManualCallback{
		Provider: "codex", SessionID: "sess-1",
		CallbackURL: "http://localhost:1455/auth/callback?code=the-code&state=" + state,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cred.ID != "oauth-codex" || cred.Kind != KindOAuth {
		t.Fatalf("credential = %+v", cred)
	}
	if !strings.Contains(gotForm, "code=the-code") || !strings.Contains(gotForm, "code_verifier=") {
		t.Fatalf("exchange body did not carry the code and PKCE verifier: %s", gotForm)
	}

	attrs, err := f.s.Attributes(adminCtx(), "oauth-codex")
	if err != nil {
		t.Fatal(err)
	}
	if attrs.AccountID != "acct-9" || attrs.PlanType != "pro" {
		t.Fatalf("attributes = %+v, want the claims from the id_token", attrs)
	}

	// The state is spent. A replay of the same pasted URL must not mint a second
	// credential, whatever the provider would say to a second exchange.
	_, err = f.s.CompleteOAuthManual(adminCtx(), OAuthManualCallback{
		Provider: "codex", SessionID: "sess-1",
		CallbackURL: "http://localhost:1455/auth/callback?code=the-code&state=" + state,
	})
	if !errors.Is(err, ErrOAuthState) {
		t.Fatalf("replayed callback err = %v, want ErrOAuthState", err)
	}
}

func TestManualCallbackIsBoundToTheSessionThatStartedIt(t *testing.T) {
	f := newFixture(t, subscriptionProvider("https://auth.example/oauth/token"), nil)
	_, state, err := f.s.BeginOAuth(adminCtx(), OAuthStart{Provider: "codex", SessionID: "sess-1"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.CompleteOAuthManual(adminCtx(), OAuthManualCallback{
		Provider: "codex", SessionID: "someone-else",
		CallbackURL: "http://localhost:1455/auth/callback?code=c&state=" + state,
	})
	if !errors.Is(err, ErrOAuthState) {
		t.Fatalf("err = %v, want ErrOAuthState", err)
	}
}

func TestParseCallbackURL(t *testing.T) {
	code, state, err := parseCallbackURL("  http://localhost:1455/auth/callback?code=abc&state=xyz  ")
	if err != nil || code != "abc" || state != "xyz" {
		t.Fatalf("code=%q state=%q err=%v", code, state, err)
	}
	if _, _, err := parseCallbackURL("http://localhost:1455/auth/callback?state=xyz"); !errors.Is(err, ErrCallbackURL) {
		t.Fatalf("err = %v, want ErrCallbackURL", err)
	}
	// A provider that refused says so, instead of surfacing as a generic state failure.
	_, _, err = parseCallbackURL("http://localhost:1455/auth/callback?error=access_denied&state=x")
	if !errors.Is(err, ErrCallbackURL) || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("err = %v, want the provider's reason", err)
	}
}

func TestImportAdoptsTokensAndDerivesExpiryFromTheAccessToken(t *testing.T) {
	f := newFixture(t, subscriptionProvider("https://auth.example/oauth/token"), nil)
	exp := time.Now().Add(45 * time.Minute)
	access := unsignedJWT(map[string]any{"exp": exp.Unix()})

	cred, err := f.s.ImportOAuth(adminCtx(), OAuthImport{
		Provider:     "codex",
		AccessToken:  SecretInput{Value: access},
		RefreshToken: SecretInput{Value: "rt"},
		IDToken:      SecretInput{Value: chatGPTIDToken("acct-import", "plus")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cred.ExpiresAt == nil {
		t.Fatal("no expiry was derived; the credential would never refresh")
	}
	if d := cred.ExpiresAt.Sub(exp.UTC()); d > time.Second || d < -time.Second {
		t.Fatalf("expiry = %v, want the access token's own exp claim (%v)", cred.ExpiresAt, exp.UTC())
	}
	attrs, err := f.s.Attributes(adminCtx(), "oauth-codex")
	if err != nil {
		t.Fatal(err)
	}
	if attrs.AccountID != "acct-import" {
		t.Fatalf("attributes = %+v", attrs)
	}

	// The still-valid access token is handed back without contacting the provider.
	tok, err := f.s.Token(adminCtx(), "oauth-codex")
	if err != nil {
		t.Fatal(err)
	}
	if tok != access {
		t.Fatalf("token = %q, want the imported access token", tok)
	}
}

func TestImportRequiresARefreshToken(t *testing.T) {
	f := newFixture(t, subscriptionProvider("https://auth.example/oauth/token"), nil)
	_, err := f.s.ImportOAuth(adminCtx(), OAuthImport{
		Provider: "codex", AccessToken: SecretInput{Value: "at"},
	})
	if !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("err = %v, want ErrInvalidSecret", err)
	}
}

func TestRefreshUsesJSONAndKeepsTheAccountAcrossRenewal(t *testing.T) {
	var contentType, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		// A refresh response that repeats neither the id_token nor the refresh token,
		// which is what the provider actually does.
		_, _ = w.Write([]byte(`{"access_token":"fresh-at","expires_in":3600}`))
	}))
	defer srv.Close()

	f := newFixture(t, subscriptionProvider(srv.URL), nil)
	// An access token already past its expiry, so the first use must refresh.
	stale := unsignedJWT(map[string]any{"exp": time.Now().Add(-time.Minute).Unix()})
	if _, err := f.s.ImportOAuth(adminCtx(), OAuthImport{
		Provider:     "codex",
		AccessToken:  SecretInput{Value: stale},
		RefreshToken: SecretInput{Value: "rt-original"},
		IDToken:      SecretInput{Value: chatGPTIDToken("acct-keep", "pro")},
	}); err != nil {
		t.Fatal(err)
	}

	tok, err := f.s.Token(adminCtx(), "oauth-codex")
	if err != nil {
		t.Fatal(err)
	}
	if tok != "fresh-at" {
		t.Fatalf("token = %q, want the refreshed one", tok)
	}
	if contentType != "application/json" {
		t.Fatalf("refresh content type = %q, want JSON", contentType)
	}
	var sent map[string]string
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("refresh body was not JSON: %s", body)
	}
	if sent["grant_type"] != "refresh_token" || sent["refresh_token"] != "rt-original" {
		t.Fatalf("refresh body = %v", sent)
	}

	attrs, err := f.s.Attributes(adminCtx(), "oauth-codex")
	if err != nil {
		t.Fatal(err)
	}
	if attrs.AccountID != "acct-keep" {
		t.Fatalf("account id = %q; a refresh must not lose it", attrs.AccountID)
	}
}

func TestAttributesNeverReturnSecretMaterial(t *testing.T) {
	f := newFixture(t, subscriptionProvider("https://auth.example/oauth/token"), nil)
	if _, err := f.s.ImportOAuth(adminCtx(), OAuthImport{
		Provider:     "codex",
		AccessToken:  SecretInput{Value: "super-secret-access"},
		RefreshToken: SecretInput{Value: "super-secret-refresh"},
		IDToken:      SecretInput{Value: chatGPTIDToken("acct-1", "pro")},
		ExpiresIn:    3600,
	}); err != nil {
		t.Fatal(err)
	}
	attrs, err := f.s.Attributes(adminCtx(), "oauth-codex")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(attrs)
	for _, secret := range []string{"super-secret-access", "super-secret-refresh"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("attributes leaked %q: %s", secret, raw)
		}
	}
}
