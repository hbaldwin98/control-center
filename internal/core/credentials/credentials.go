// Package credentials stores API keys and OAuth credentials, exposes secret material only
// to authorized runtime modules, and exposes secret-free administration to the web module.
//
// Layer 2. It imports storage and events. Plugins never receive either interface.
package credentials

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/storage"
)

type Kind string

const (
	KindAPIKey Kind = "api_key"
	KindOAuth  Kind = "oauth"
)

type Status string

const (
	StatusOK          Status = "ok"
	StatusExpiring    Status = "expiring"
	StatusNeedsReauth Status = "needs_reauth"
)

// Credential is the secret-free view. Admin has no method that returns secret material.
type Credential struct {
	ID        string     `json:"id"`
	Kind      Kind       `json:"kind"`
	Provider  string     `json:"provider"`
	Status    Status     `json:"status"`
	Version   int64      `json:"version"`
	ExpiresAt *time.Time `json:"expiresAt"`
	Scopes    []string   `json:"scopes"`
}

// Attributes is the secret-free description of a stored credential that a runtime
// consumer may need in order to shape its provider request — most importantly the
// account id a subscription-backed provider requires on every call.
type Attributes struct {
	Provider  string
	Kind      Kind
	AccountID string
	PlanType  string
}

// Runtime is never imported by web. Token returns an API key or a valid OAuth access
// token, refreshing OAuth credentials when needed.
type Runtime interface {
	Token(ctx context.Context, id string) (string, error)
	Attributes(ctx context.Context, id string) (Attributes, error)
}

// ReferenceStore is used by credential-consuming core modules. Owners are stable names
// such as `ai.routes` or `notifications.channel:<id>`.
type ReferenceStore interface {
	Replace(ctx context.Context, owner string, credentialIDs []string) error
	ReplaceTx(ctx context.Context, tx storage.Tx, owner string, credentialIDs []string) error
}

// SecretInput is request-only. It must never appear in logs, errors, events, or Admin
// responses.
type SecretInput struct {
	Value string
}

type APIKeyInput struct {
	ID       string
	Provider string
	Secret   SecretInput
}

type OAuthStart struct {
	Provider    string
	SessionID   string
	RedirectURI string
}

type OAuthCallback struct {
	Provider    string
	SessionID   string
	RedirectURI string
	State       string
	Code        string
}

// OAuthManualCallback completes a flow whose redirect the provider pins to an address
// this server cannot receive. The administrator pastes the URL their browser landed
// on; every binding the ordinary callback checks is checked here too.
type OAuthManualCallback struct {
	Provider    string
	SessionID   string
	CallbackURL string
}

// OAuthImport adopts tokens minted by another client for the same provider, such as a
// local `codex login`. RefreshToken is required: an access token alone expires within
// the hour and leaves nothing to renew from.
type OAuthImport struct {
	Provider     string
	AccessToken  SecretInput
	RefreshToken SecretInput
	IDToken      SecretInput
	ExpiresIn    int
}

// Admin accepts new secret material but has no method that returns it.
type Admin interface {
	List(ctx context.Context) ([]Credential, error)
	References(ctx context.Context, id string) ([]string, error)
	Delete(ctx context.Context, id string) error
	CreateAPIKey(ctx context.Context, in APIKeyInput) (Credential, error)
	ReplaceAPIKey(ctx context.Context, id string, secret SecretInput) (Credential, error)
	RotateAPIKey(ctx context.Context, id string, next SecretInput) (Credential, error)
	BeginOAuth(ctx context.Context, in OAuthStart) (authURL, state string, err error)
	CompleteOAuth(ctx context.Context, in OAuthCallback) (Credential, error)
	CompleteOAuthManual(ctx context.Context, in OAuthManualCallback) (Credential, error)
	ImportOAuth(ctx context.Context, in OAuthImport) (Credential, error)
}

var (
	ErrNeedsReauth       = errors.New("credential needs reauthorization")
	ErrCredentialInUse   = errors.New("credential is in use")
	ErrUnknownCredential = errors.New("credentials: unknown id")
	ErrNoActor           = errors.New("credentials: authenticated actor required")
	ErrUnknownProvider   = errors.New("credentials: unknown oauth provider")
	ErrOAuthState        = errors.New("credentials: oauth state is invalid or expired")
	ErrRedirect          = errors.New("credentials: redirect uri is not allowlisted")
	ErrInvalidID         = errors.New("credentials: invalid id")
	ErrInvalidSecret     = errors.New("credentials: secret is empty")
	ErrCallbackURL       = errors.New("credentials: callback url has no authorization code")
	ErrNotManual         = errors.New("credentials: provider does not use a manual callback")
	ErrKindMismatch      = errors.New("credentials: credential kind mismatch")
	ErrDuplicateID       = errors.New("credentials: id already exists")
	ErrMasterKey         = errors.New("credentials: master key is absent or malformed")
	ErrEnvelope          = errors.New("credentials: envelope failed authentication")
)

type actorKey struct{}

// WithActor stamps the authenticated principal. Admin mutations reject a context
// without one. Background work uses ActorSystem.
func WithActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

// ActorSystem is recorded for refresh and other host-initiated mutations.
const ActorSystem = "system"

func actor(ctx context.Context) (string, error) {
	s, _ := ctx.Value(actorKey{}).(string)
	if s == "" {
		return "", ErrNoActor
	}
	return s, nil
}

// ParseMasterKey decodes a 32-byte AES key from hex. Startup fails closed without one.
func ParseMasterKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		return nil, ErrMasterKey
	}
	return b, nil
}
