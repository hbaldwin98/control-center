# credentials

**Layer 2** · `internal/core/credentials` · imports `storage`, `events` · used by `ai` and
`notifications` for runtime secrets, and by `web` for administration

**Responsibility.** Store API keys and OAuth credentials, expose secret material only to
authorized runtime modules, and expose secret-free administration to the web module.

Plugins never receive either interface. The runtime interfaces injected into `ai` and
`notifications` are restricted to the credential IDs configured for those modules.

---

## Interface

```go
package credentials

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

type Credential struct {
    ID        string
    Kind      Kind
    Provider  string
    Status    Status
    Version   int64
    ExpiresAt *time.Time
    Scopes    []string
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

type SecretInput struct {
    Value string // request-only; redacted from logs and errors
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
    ErrNeedsReauth   = errors.New("credential needs reauthorization")
    ErrCredentialInUse = errors.New("credential is in use")
)
```

Every `Admin` mutation derives the authenticated actor from context; background actions
use an explicit system principal. Calls without an authenticated actor are rejected. This
applies uniformly to create, replace, rotate, delete, and OAuth completion, and supplies
the actor stored in `core_credential_audit`.

`Delete` checks `core_credential_references` in the deletion transaction and returns
`ErrCredentialInUse` with the secret-free owner names when any reference remains. AI
replaces its complete route reference set after validating `models.yaml`; notifications
updates a channel's reference with `ReplaceTx` in the same transaction as channel config.
This makes deletion checks atomic without requiring credentials to import either higher
layer. The Settings UI shows references and requires the administrator to remove them
before deletion.

`CreateAPIKey` creates version 1. `ReplaceAPIKey` immediately and atomically replaces
invalid or unavailable material without provider validation. `RotateAPIKey` validates the
next key first, then atomically activates a new version and retires the old encrypted
version; failed validation leaves the old key active. Neither operation can return the
old or new secret. Audit metadata records the actor and version change.

Notification channel tokens may be ordinary API-key entries. The notifications module
resolves only the credential IDs referenced by its channel configuration through its
scoped `Runtime`; channel tables never contain plaintext secrets.

---

## OAuth

`BeginOAuth` requires the provider, authenticated web session ID, and an allowlisted
redirect URI. It creates a cryptographically random 256-bit state value and PKCE verifier,
uses the S256 challenge in the authorization URL, and persists the following server-side
record before returning:

```
state_hash, session_hash, provider, redirect_uri, verifier_envelope, expires_at, consumed_at
```

The state expires after 10 minutes and survives process restarts. The callback supplies
the same web session, provider, redirect URI, state, and authorization code. Completion
hashes and looks up state, verifies every binding and expiry, and marks it consumed in a
transaction before exchanging the code. Consumption is one-time even if the exchange
fails; the user starts a new flow. The raw state is returned only to the initiating
browser, and the PKCE verifier never leaves the server.

After token exchange, credential insertion and any required event use one transaction
with `events.PublishTx`. Redirect matching is exact after URI normalization; arbitrary
callback URLs are never accepted.

---

## Encryption And Rotation

Every stored secret uses an AES-256-GCM envelope with these fields:

```
format_version, key_version, nonce, ciphertext, tag
```

The master key is exactly 32 bytes and comes from the environment or OS keyring. Each
encryption uses a fresh random 96-bit nonce and a 128-bit authentication tag. A canonical
length-prefixed AAD encoding binds the table, credential or OAuth-state ID, secret kind,
provider, credential version, and key version. Metadata changes therefore cannot move a
ciphertext to another record or purpose.

Startup fails closed if the active key is absent or malformed, a referenced key version
is unavailable, or any envelope fails authentication. The service never starts with a
partially readable credential set.

Master-key rotation adds a new key version, makes it active for new writes, and
transactionally re-encrypts credential secret envelopes and pending OAuth verifier rows
with fresh nonces. Old key versions remain available until no envelope references them;
only then may they be removed. Credential `Version` tracks secret replacement and is
separate from envelope `key_version`.

Application backups never embed master keys. A restore is usable only with every key
version referenced by the snapshot, and restore validation authenticates all envelopes
before the restored service is made available.

Plaintext is held only for the provider call or encryption operation and is never logged,
placed in configuration, returned by `Admin`, or included in events.

---

## Refresh And Events

A background refresher runs ahead of OAuth expiry. `Runtime.Token` never returns an
expired token; concurrent callers share one refresh and wait rather than receiving stale
material. On unrecoverable failure, credentials atomically marks the credential
`needs_reauth` and publishes `core.credential.needs_reauth` with `PublishTx`.

Refresh updates use the credential version as a compare-and-swap guard, so refresh-token
rotation is atomic and a concurrent delete cannot resurrect the row. Transient network,
rate-limit, and provider 5xx failures retain a still-unexpired access token and retry with
bounded backoff. `invalid_grant`, revoked consent, or an expired token that cannot refresh
is unrecoverable. Reauthorization replaces the credential version and restores `ok`.
`Token` guarantees local expiry and refresh rules, not that a provider has not revoked a
token out of band.

---

## Tables

```
core_credentials(id, kind, provider, status, version, expires_at, scopes,
                 secret_envelope, created_at, updated_at)
core_credential_oauth_states(state_hash, session_hash, provider, redirect_uri,
                             verifier_envelope, expires_at, consumed_at, created_at)
core_credential_audit(id, credential_id, actor, action, version, created_at)
core_credential_references(owner, credential_id, updated_at)
```

Subscription-backed provider credentials remain ordinary OAuth credentials behind a
provider adapter. Their product scope, model access, rate limits, and terms must be
verified before use.
