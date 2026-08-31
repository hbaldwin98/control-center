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

// Runtime is never imported by web. Token returns an API key or a valid OAuth access
// token, refreshing OAuth credentials when needed.
type Runtime interface {
	Token(ctx context.Context, id string) (string, error)
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
