package credentials

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

type oauthSecret struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	Expiry       time.Time `json:"expiry"`
}

func (t oauthSecret) validAt(at time.Time) bool {
	return t.AccessToken != "" && (t.Expiry.IsZero() || at.Before(t.Expiry))
}

func decodeOAuthSecret(plain []byte) (oauthSecret, error) {
	var t oauthSecret
	if err := json.Unmarshal(plain, &t); err != nil {
		return oauthSecret{}, ErrEnvelope
	}
	return t, nil
}

func normalizeRedirect(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", ErrRedirect
	}
	u.Fragment = ""
	// Exact match after normalization: lowercase scheme/host, keep path, drop default ports.
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	if (u.Scheme == "http" && u.Port() == "80") || (u.Scheme == "https" && u.Port() == "443") {
		u.Host = u.Hostname()
	}
	return u.String(), nil
}

func (s *Store) allowRedirect(raw string) (string, error) {
	n, err := normalizeRedirect(raw)
	if err != nil {
		return "", err
	}
	if _, ok := s.allowed[n]; !ok {
		return "", ErrRedirect
	}
	return n, nil
}

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func randomB64(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *Store) BeginOAuth(ctx context.Context, in OAuthStart) (authURL, state string, err error) {
	if _, err := actor(ctx); err != nil {
		return "", "", err
	}
	prov, ok := s.oauth[in.Provider]
	if !ok {
		return "", "", ErrUnknownProvider
	}
	redirect, err := s.allowRedirect(in.RedirectURI)
	if err != nil {
		return "", "", err
	}
	if in.SessionID == "" {
		return "", "", ErrOAuthState
	}
	state, err = randomB64(32)
	if err != nil {
		return "", "", err
	}
	verifier, err := randomB64(32)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	env, err := s.encrypt([]byte(verifier), "core_credential_oauth_states", hashHex(state), string(KindOAuth), in.Provider, 0)
	if err != nil {
		return "", "", err
	}
	now := s.now().UTC()
	err = s.db.Tx(ctx, func(tx storage.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO core_credential_oauth_states(
			     state_hash, session_hash, provider, redirect_uri, verifier_envelope, expires_at, consumed_at, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, NULL, ?)`,
			hashHex(state), hashHex(in.SessionID), in.Provider, redirect, env,
			rfc(now.Add(10*time.Minute)), rfc(now))
		return err
	})
	if err != nil {
		return "", "", err
	}

	u, err := url.Parse(prov.AuthURL)
	if err != nil {
		return "", "", err
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", prov.ClientID)
	q.Set("redirect_uri", redirect)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	if len(prov.Scopes) > 0 {
		q.Set("scope", strings.Join(prov.Scopes, " "))
	}
	u.RawQuery = q.Encode()
	return u.String(), state, nil
}

func (s *Store) CompleteOAuth(ctx context.Context, in OAuthCallback) (Credential, error) {
	actor, err := actor(ctx)
	if err != nil {
		return Credential{}, err
	}
	redirect, err := s.allowRedirect(in.RedirectURI)
	if err != nil {
		return Credential{}, err
	}
	if in.State == "" || in.Code == "" || in.SessionID == "" {
		return Credential{}, ErrOAuthState
	}

	var verifier, provider string
	err = s.db.Tx(ctx, func(tx storage.Tx) error {
		var sessionHash, storedProvider, storedRedirect, env, expires string
		var consumed *string
		err := tx.QueryRow(ctx,
			`SELECT session_hash, provider, redirect_uri, verifier_envelope, expires_at, consumed_at
			   FROM core_credential_oauth_states WHERE state_hash = ?`, hashHex(in.State)).
			Scan(&sessionHash, &storedProvider, &storedRedirect, &env, &expires, &consumed)
		if storage.IsNoRows(err) {
			return ErrOAuthState
		}
		if err != nil {
			return err
		}
		if consumed != nil {
			return ErrOAuthState
		}
		exp, err := time.Parse(time.RFC3339Nano, expires)
		if err != nil || !s.now().UTC().Before(exp) {
			return ErrOAuthState
		}
		if sessionHash != hashHex(in.SessionID) || storedRedirect != redirect {
			return ErrOAuthState
		}
		if in.Provider != "" && storedProvider != in.Provider {
			return ErrOAuthState
		}
		provider = storedProvider
		plain, err := s.decrypt(env, "core_credential_oauth_states", hashHex(in.State), string(KindOAuth), storedProvider, 0)
		if err != nil {
			return err
		}
		verifier = string(plain)
		_, err = tx.Exec(ctx,
			`UPDATE core_credential_oauth_states SET consumed_at = ? WHERE state_hash = ?`,
			rfc(s.now()), hashHex(in.State))
		return err
	})
	if err != nil {
		return Credential{}, err
	}

	prov, ok := s.oauth[provider]
	if !ok {
		return Credential{}, ErrUnknownProvider
	}
	tok, err := s.exchangeCode(ctx, prov, redirect, in.Code, verifier)
	if err != nil {
		return Credential{}, err
	}

	id := "oauth-" + provider
	var out Credential
	err = s.db.Tx(ctx, func(tx storage.Tx) error {
		existing, readErr := readTx(ctx, tx, id)
		isNew := readErr != nil && readErr == ErrUnknownCredential
		if readErr != nil && !isNew {
			return readErr
		}
		version := int64(1)
		if !isNew {
			version = existing.version + 1
		}
		raw, err := json.Marshal(tok)
		if err != nil {
			return err
		}
		env, err := s.encrypt(raw, "core_credentials", id, string(KindOAuth), provider, version)
		if err != nil {
			return err
		}
		now := rfc(s.now())
		var exp any
		if !tok.Expiry.IsZero() {
			exp = rfc(tok.Expiry)
		}
		scopes := marshalScopes(prov.Scopes)
		if isNew {
			_, err = tx.Exec(ctx,
				`INSERT INTO core_credentials(id, kind, provider, status, version, expires_at, scopes, secret_envelope, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				id, string(KindOAuth), provider, string(StatusOK), version, exp, scopes, env, now, now)
		} else {
			_, err = tx.Exec(ctx,
				`UPDATE core_credentials SET secret_envelope = ?, version = ?, status = ?, expires_at = ?, scopes = ?, updated_at = ?
				 WHERE id = ?`, env, version, string(StatusOK), exp, scopes, now, id)
		}
		if err != nil {
			return err
		}
		if err := s.audit(ctx, tx, id, actor, "oauth", version); err != nil {
			return err
		}
		out = Credential{ID: id, Kind: KindOAuth, Provider: provider, Status: StatusOK, Version: version, Scopes: prov.Scopes}
		if !tok.Expiry.IsZero() {
			t := tok.Expiry.UTC()
			out.ExpiresAt = &t
		}
		return nil
	})
	return out, err
}

func errorsIsUnknown(err error) bool {
	return err == ErrUnknownCredential
}

func (s *Store) exchangeCode(ctx context.Context, prov OAuthProvider, redirect, code, verifier string) (oauthSecret, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirect)
	form.Set("client_id", prov.ClientID)
	form.Set("code_verifier", verifier)
	if prov.ClientSecret != "" {
		form.Set("client_secret", prov.ClientSecret)
	}
	return s.tokenRequest(ctx, prov.TokenURL, form)
}

func (s *Store) tokenRequest(ctx context.Context, tokenURL string, form url.Values) (oauthSecret, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return oauthSecret{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := s.http.Do(req)
	if err != nil {
		return oauthSecret{}, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return oauthSecret{}, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return oauthSecret{}, fmt.Errorf("credentials: token endpoint status %d", res.StatusCode)
	}
	var raw struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return oauthSecret{}, err
	}
	if raw.Error == "invalid_grant" || raw.AccessToken == "" {
		return oauthSecret{}, ErrNeedsReauth
	}
	tok := oauthSecret{AccessToken: raw.AccessToken, RefreshToken: raw.RefreshToken, TokenType: raw.TokenType}
	if raw.ExpiresIn > 0 {
		tok.Expiry = s.now().UTC().Add(time.Duration(raw.ExpiresIn) * time.Second)
	}
	return tok, nil
}

func (s *Store) lock(id string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.refresh[id]
	if !ok {
		m = &sync.Mutex{}
		s.refresh[id] = m
	}
	return m
}

func (s *Store) refreshOAuth(ctx context.Context, row credRow, current oauthSecret) (string, error) {
	lk := s.lock(row.id)
	lk.Lock()
	defer lk.Unlock()

	// Another caller may have refreshed while we waited.
	fresh, err := s.read(ctx, row.id)
	if err != nil {
		return "", err
	}
	plain, err := s.decrypt(fresh.env, "core_credentials", fresh.id, string(fresh.kind), fresh.provider, fresh.version)
	if err != nil {
		return "", err
	}
	tok, err := decodeOAuthSecret(plain)
	if err != nil {
		return "", err
	}
	if tok.validAt(s.now().UTC().Add(30 * time.Second)) {
		return tok.AccessToken, nil
	}
	if tok.RefreshToken == "" {
		_ = s.markNeedsReauth(ctx, fresh)
		return "", ErrNeedsReauth
	}
	prov, ok := s.oauth[fresh.provider]
	if !ok {
		return "", ErrUnknownProvider
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", tok.RefreshToken)
	form.Set("client_id", prov.ClientID)
	if prov.ClientSecret != "" {
		form.Set("client_secret", prov.ClientSecret)
	}
	next, err := s.tokenRequest(ctx, prov.TokenURL, form)
	if err != nil {
		if errorsIs(err, ErrNeedsReauth) {
			_ = s.markNeedsReauth(ctx, fresh)
		}
		return "", err
	}
	if next.RefreshToken == "" {
		next.RefreshToken = tok.RefreshToken
	}
	if err := s.saveOAuth(ctx, fresh, next, "refresh"); err != nil {
		return "", err
	}
	return next.AccessToken, nil
}

func errorsIs(err, target error) bool {
	return err != nil && (err == target || strings.Contains(err.Error(), target.Error()))
}

func (s *Store) saveOAuth(ctx context.Context, row credRow, tok oauthSecret, action string) error {
	next := row.version + 1
	raw, err := json.Marshal(tok)
	if err != nil {
		return err
	}
	env, err := s.encrypt(raw, "core_credentials", row.id, string(KindOAuth), row.provider, next)
	if err != nil {
		return err
	}
	return s.db.Tx(ctx, func(tx storage.Tx) error {
		var version int64
		err := tx.QueryRow(ctx, `SELECT version FROM core_credentials WHERE id = ?`, row.id).Scan(&version)
		if storage.IsNoRows(err) {
			return ErrUnknownCredential
		}
		if err != nil {
			return err
		}
		if version != row.version {
			return nil // lost the CAS; another refresh won
		}
		var exp any
		if !tok.Expiry.IsZero() {
			exp = rfc(tok.Expiry)
		}
		now := rfc(s.now())
		if _, err := tx.Exec(ctx,
			`UPDATE core_credentials SET secret_envelope = ?, version = ?, status = ?, expires_at = ?, updated_at = ?
			 WHERE id = ? AND version = ?`,
			env, next, string(StatusOK), exp, now, row.id, row.version); err != nil {
			return err
		}
		return s.audit(ctx, tx, row.id, ActorSystem, action, next)
	})
}

func (s *Store) markNeedsReauth(ctx context.Context, row credRow) error {
	return s.db.Tx(ctx, func(tx storage.Tx) error {
		now := rfc(s.now())
		res, err := tx.Exec(ctx,
			`UPDATE core_credentials SET status = ?, updated_at = ? WHERE id = ? AND version = ?`,
			string(StatusNeedsReauth), now, row.id, row.version)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return nil
		}
		if err := s.audit(ctx, tx, row.id, ActorSystem, "needs_reauth", row.version); err != nil {
			return err
		}
		_, err = s.bus.PublishTx(ctx, tx, events.Input{
			Type:    events.TypeCredentialNeedsReauth,
			Source:  events.SourceCredentials,
			Subject: row.id,
			Payload: map[string]any{"provider": row.provider, "credentialId": row.id},
		})
		return err
	})
}
