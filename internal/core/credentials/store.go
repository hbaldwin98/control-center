package credentials

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

// OAuthProvider is one authorization-code + PKCE endpoint pair.
type OAuthProvider struct {
	AuthURL      string
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scopes       []string
}

// Options configures encryption, OAuth, and clocks.
type Options struct {
	// Keys maps envelope key_version to a 32-byte AES key. Active is used for new
	// writes. Startup fails if any stored envelope references a missing version.
	Keys   map[int][]byte
	Active int

	AllowedRedirectURIs []string
	OAuth               map[string]OAuthProvider

	HTTPClient *http.Client
	Now        func() time.Time
}

// Store is the concrete Runtime, Admin, and ReferenceStore.
type Store struct {
	db     storage.DB
	bus    events.Bus
	keys   map[int][]byte
	active int
	now    func() time.Time
	http   *http.Client

	allowed map[string]struct{}
	oauth   map[string]OAuthProvider

	mu      sync.Mutex
	refresh map[string]*sync.Mutex
}

var (
	_ Runtime        = (*Store)(nil)
	_ Admin          = (*Store)(nil)
	_ ReferenceStore = (*Store)(nil)
)

func New(m storage.Migrator, db storage.DB, bus events.Bus, opts Options) (*Store, error) {
	if err := m.Apply("credentials", migrations); err != nil {
		return nil, err
	}
	if opts.Active == 0 {
		opts.Active = 1
	}
	if len(opts.Keys) == 0 || len(opts.Keys[opts.Active]) != 32 {
		return nil, ErrMasterKey
	}
	for ver, key := range opts.Keys {
		if len(key) != 32 {
			return nil, fmt.Errorf("%w: version %d", ErrMasterKey, ver)
		}
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	client := opts.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	allowed := make(map[string]struct{}, len(opts.AllowedRedirectURIs))
	for _, u := range opts.AllowedRedirectURIs {
		if n, err := normalizeRedirect(u); err == nil {
			allowed[n] = struct{}{}
		}
	}
	oauth := opts.OAuth
	if oauth == nil {
		oauth = map[string]OAuthProvider{}
	}
	s := &Store{
		db: db, bus: bus, keys: opts.Keys, active: opts.Active, now: now, http: client,
		allowed: allowed, oauth: oauth, refresh: map[string]*sync.Mutex{},
	}
	if err := s.verifyAll(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

// OAuthProviders returns configured provider names, secret-free, for the Settings UI.
func (s *Store) OAuthProviders() []string {
	names := make([]string, 0, len(s.oauth))
	for n := range s.oauth {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (s *Store) verifyAll(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `SELECT id, kind, provider, version, secret_envelope FROM core_credentials`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, kind, provider, env string
		var version int64
		if err := rows.Scan(&id, &kind, &provider, &version, &env); err != nil {
			return err
		}
		if _, err := s.decrypt(env, "core_credentials", id, kind, provider, version); err != nil {
			return fmt.Errorf("credentials: %s: %w", id, err)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	states, err := s.db.Query(ctx, `SELECT state_hash, provider, verifier_envelope FROM core_credential_oauth_states WHERE consumed_at IS NULL`)
	if err != nil {
		return err
	}
	defer states.Close()
	for states.Next() {
		var hash, provider, env string
		if err := states.Scan(&hash, &provider, &env); err != nil {
			return err
		}
		if _, err := s.decrypt(env, "core_credential_oauth_states", hash, string(KindOAuth), provider, 0); err != nil {
			return fmt.Errorf("credentials: oauth state %s: %w", hash, err)
		}
	}
	return states.Err()
}

// Token returns an API key or a still-valid OAuth access token.
func (s *Store) Token(ctx context.Context, id string) (string, error) {
	row, err := s.read(ctx, id)
	if err != nil {
		return "", err
	}
	plain, err := s.decrypt(row.env, "core_credentials", row.id, string(row.kind), row.provider, row.version)
	if err != nil {
		return "", err
	}
	switch row.kind {
	case KindAPIKey:
		return string(plain), nil
	case KindOAuth:
		tok, err := decodeOAuthSecret(plain)
		if err != nil {
			return "", err
		}
		if tok.validAt(s.now().UTC().Add(30 * time.Second)) {
			return tok.AccessToken, nil
		}
		return s.refreshOAuth(ctx, row, tok)
	default:
		return "", fmt.Errorf("credentials: unknown kind %q", row.kind)
	}
}

func (s *Store) Replace(ctx context.Context, owner string, credentialIDs []string) error {
	return s.db.Tx(ctx, func(tx storage.Tx) error {
		return s.ReplaceTx(ctx, tx, owner, credentialIDs)
	})
}

func (s *Store) ReplaceTx(ctx context.Context, tx storage.Tx, owner string, credentialIDs []string) error {
	if owner == "" {
		return fmt.Errorf("credentials: empty reference owner")
	}
	for _, id := range credentialIDs {
		if _, err := readTx(ctx, tx, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM core_credential_references WHERE owner = ?`, owner); err != nil {
		return err
	}
	now := rfc(s.now())
	for _, id := range credentialIDs {
		if _, err := tx.Exec(ctx,
			`INSERT INTO core_credential_references(owner, credential_id, updated_at) VALUES (?, ?, ?)`,
			owner, id, now); err != nil {
			return err
		}
	}
	return nil
}

type credRow struct {
	id       string
	kind     Kind
	provider string
	status   Status
	version  int64
	expires  *time.Time
	scopes   []string
	env      string
}

func (s *Store) read(ctx context.Context, id string) (credRow, error) {
	return scanCred(s.db.QueryRow(ctx,
		`SELECT id, kind, provider, status, version, expires_at, scopes, secret_envelope
		   FROM core_credentials WHERE id = ?`, id))
}

func readTx(ctx context.Context, tx storage.Tx, id string) (credRow, error) {
	return scanCred(tx.QueryRow(ctx,
		`SELECT id, kind, provider, status, version, expires_at, scopes, secret_envelope
		   FROM core_credentials WHERE id = ?`, id))
}

type scanner interface {
	Scan(dest ...any) error
}

func scanCred(row scanner) (credRow, error) {
	var r credRow
	var kind, status, scopes string
	var exp *string
	err := row.Scan(&r.id, &kind, &r.provider, &status, &r.version, &exp, &scopes, &r.env)
	if storage.IsNoRows(err) {
		return credRow{}, ErrUnknownCredential
	}
	if err != nil {
		return credRow{}, err
	}
	r.kind = Kind(kind)
	r.status = Status(status)
	if exp != nil && *exp != "" {
		if t, err := time.Parse(time.RFC3339Nano, *exp); err == nil {
			r.expires = &t
		}
	}
	_ = json.Unmarshal([]byte(scopes), &r.scopes)
	if r.scopes == nil {
		r.scopes = []string{}
	}
	return r, nil
}

func (r credRow) public() Credential {
	return Credential{
		ID: r.id, Kind: r.kind, Provider: r.provider, Status: r.status,
		Version: r.version, ExpiresAt: r.expires, Scopes: r.scopes,
	}
}

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func validID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for i, c := range id {
		ok := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-'
		if i == 0 && (c < 'a' || c > 'z') {
			return false
		}
		if !ok {
			return false
		}
	}
	return true
}
