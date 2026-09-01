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
// token, refreshing OAuth credentials when needed. Attributes returns the secret-free
// facts a caller needs to address the provider, such as the account a subscription
// belongs to.
type Runtime interface {
    Token(ctx context.Context, id string) (string, error)
    Attributes(ctx context.Context, id string) (Attributes, error)
}

// Attributes carries no secret material. AccountID and PlanType are read from the
// provider's id_token and are routing and display metadata only.
type Attributes struct {
    Provider  string
    Kind      Kind
    AccountID string
    PlanType  string
}

// ReferenceStore is used by credential-consuming core modules. Owners are stable names
// such as `ai.providers` or `notifications.channel:<id>`.
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
    // CompleteOAuthManual finishes a flow whose redirect this server cannot receive.
    CompleteOAuthManual(ctx context.Context, in OAuthManualCallback) (Credential, error)
    // ImportOAuth adopts tokens another client already minted for the same provider.
    ImportOAuth(ctx context.Context, in OAuthImport) (Credential, error)
    OAuthProviderList() []OAuthProviderInfo
}

type OAuthManualCallback struct {
    Provider    string
    SessionID   string
    CallbackURL string // the whole address the browser landed on
}

type OAuthImport struct {
    Provider     string
    AccessToken  SecretInput
    RefreshToken SecretInput // required
    IDToken      SecretInput
    ExpiresIn    int
}

// OAuthProviderInfo describes a provider to the UI. It never carries a client secret.
type OAuthProviderInfo struct {
    Name        string
    Manual      bool   // the provider pins a redirect this server cannot receive
    RedirectURI string
    Scopes      []string
    Importable  bool
}

var (
    ErrNeedsReauth     = errors.New("credential needs reauthorization")
    ErrCredentialInUse = errors.New("credential is in use")
    ErrCallbackURL     = errors.New("callback url is not a completed authorization")
    ErrNotManual       = errors.New("provider does not use manual redirect capture")
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

### Pinned redirects and manual completion

Some providers register one fixed redirect that belongs to a local command-line client —
the ChatGPT login pins `http://localhost:1455/auth/callback` — and refuse any other. A
self-hosted control center cannot receive that callback, and rewriting it is not an
option the provider offers.

Such a provider is configured with a `RedirectURI` of its own and reports `manual` in
`OAuthProviderList`. `BeginOAuth` then builds the authorization URL against the pinned
address instead of the caller's, and records it in the state row. The browser finishes on
a page that fails to load; the administrator copies that address out of the address bar
and posts it back, and `CompleteOAuthManual` reads the code and state out of it. The
pending flow lives in the state row rather than in the page that started it, so closing
or reloading the tab mid-login does not lose it — the paste is offered for as long as a
manual provider is configured, and the state itself decides what is still valid.

Completion is gated the way the served callback is: a session, plus the one-time state
only the browser that began the flow holds. The password is spent at the beginning, which
is the step that will create a credential; requiring it again at the end would strand an
administrator whose trip through the provider's login outlasted the five-minute window.

A deployment the browser can reach *at* the pinned address needs none of that. `web`
serves the pinned path and completes the flow itself, but only when the address the
request was sent to is the one a configured manual provider is pinned to; anywhere else
the path is an ordinary frontend route and the pending state is left alone. That is a
local convenience, not a second mechanism: it consumes the same state the same way, and
an ambiguous match completes nothing rather than burning a flow to find out.

Nothing about the security of the flow is relaxed by that paste. The state is still 256
bits, still bound to the initiating web session, still expires in 10 minutes, and is still
consumed once in a transaction before the exchange. The PKCE verifier still never leaves
the server, so a code observed in the address bar cannot be redeemed anywhere else. A
callback carrying `error=` surfaces the provider's own reason rather than a generic state
failure.

### Importing tokens

`ImportOAuth` adopts tokens another client already minted for the same provider, such as a
local `codex login`. The refresh token is required: without it the credential would work
until the access token expired and then die with no way back. Expiry is taken from
`expires_in`, or from the access token's own `exp` claim, or as a last resort from now.
Both clients then share one refresh token, and whichever renews first may invalidate the
other; that is the caller's trade, and signing in through the manual flow avoids it.

Neither path verifies an `id_token` signature. The claims read from it — the ChatGPT
account id and plan type — are routing and display metadata carried on the credential and
used to address the provider. Authority over whether a token is real rests with the
provider that is asked to honor it.

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

An OAuth credential's envelope holds the access token, the refresh token, the id_token,
and the account and plan read from it. All of it is inside the one authenticated
ciphertext; `Attributes` decrypts it and returns only the non-secret fields.

Subscription-backed provider credentials remain ordinary OAuth credentials behind a
provider adapter. Their product scope, model access, rate limits, and terms must be
verified before use.
