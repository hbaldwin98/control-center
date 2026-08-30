# credentials

**Layer 2** · `internal/core/credentials` · imports `storage`, `events` · used by `ai` only

**Responsibility.** Store API keys and OAuth credentials, and hand out tokens that are
currently valid.

Plugins never reach this module. It is not on the `Host` facade, and there is no path to
it that does not go through `ai`.

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
    ID        string // "google-api", "codex-oauth"
    Kind      Kind
    Provider  string // "openai", "google", "openrouter", "codex"
    Status    Status
    ExpiresAt *time.Time
    Scopes    []string
}

type Store interface {
    // Token returns a valid token, refreshing if needed.
    // Returns ErrNeedsReauth when refresh fails and a human must intervene.
    Token(ctx context.Context, id string) (string, error)

    List(ctx context.Context) ([]Credential, error)
    Delete(ctx context.Context, id string) error

    // OAuth flow, driven by the settings UI.
    BeginOAuth(ctx context.Context, provider string) (authURL, state string, err error)
    CompleteOAuth(ctx context.Context, state, code string) (*Credential, error)
}

var ErrNeedsReauth = errors.New("credential needs reauthorization")
```

---

## Refresh

A background refresher runs ahead of expiry. On unrecoverable failure it marks the
credential `needs_reauth` and emits `core.credential.needs_reauth`, which has a default
notification rule. The settings UI shows a re-auth button per credential.

`Token` never returns an expired token. If refresh is in flight, callers block briefly
rather than receiving a stale value.

---

## Events emitted

| Event | When |
|---|---|
| `core.credential.needs_reauth` | refresh failed unrecoverably |

---

## Tables

```
core_credentials(id, kind, provider, status, expires_at, scopes, secret_enc, created_at, updated_at)
```

`secret_enc` is encrypted at rest with a key from the environment or the OS keyring.
Secrets never appear in `config/*.yaml`, which is checked in.

---

## Notes

> **On the Codex/ChatGPT subscription credential.** It is modelled as an ordinary OAuth
> credential feeding an ordinary provider adapter — nothing special in this module.
>
> Verify before relying on it: subscription-backed access is typically product-scoped, may
> be Responses-shaped rather than chat-completions-shaped, exposes a restricted model list,
> and carries plan-based rate limits. Treat the adapter as potentially lossy, and confirm
> the terms permit the use intended.
>
> If it works, it is the cheapest capacity available, which is why
> [`ai`](ai.md) supports preferring it and spilling over to a paid API on 429.
