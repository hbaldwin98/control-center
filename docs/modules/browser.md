# browser

**Layer 3** · `internal/core/browser` · imports `events`, `policy` ·
used by `pluginhost`

**Responsibility.** Own headless browser sessions for plugins: launch, allowlist, fetch,
and tear down. Plugins never import an automation library.

This is a host capability even with one consumer. A plugin-owned Playwright process is a
network and process the kill switch cannot close. The engine (Playwright, Chromium, or
anything else) is an implementation detail of this module; the plugin-facing API is the
stable surface.

---

## Interface

```go
package browser

type Browser interface {
    Open(ctx context.Context, opts OpenOptions) (Session, error)
}

type OpenOptions struct {
    // AllowedHosts is required and nonempty. Every navigation, redirect, subresource,
    // and Get is checked against it. Hosts are lowercase DNS names, no ports, no IPs.
    AllowedHosts []string
}

type Session interface {
    NewPage(ctx context.Context) (Page, error)
    Close(ctx context.Context) error
}

type Page interface {
    // Goto loads url after allowlist and private-network checks. The page's document
    // origin must remain on the allowlist.
    Goto(ctx context.Context, url string) error

    // WaitFor returns when selector matches at least one element, or ctx/timeout fires.
    WaitFor(ctx context.Context, selector string, d time.Duration) error

    // Content is the current document HTML. Bounded by the host max-document limit.
    Content(ctx context.Context) (string, error)

    // Get fetches an allowlisted URL as bytes (images, static files). It uses the
    // session's cookie jar and the same SSRF checks as Goto. Bounded by max-resource.
    Get(ctx context.Context, url string) (Resource, error)

    Close(ctx context.Context) error
}

type Resource struct {
    URL    string
    MIME   string
    Body   []byte
    Status int
}
```

Conversation and DOM state belong to the `Page`. The plugin parses HTML itself; v1 does
not expose `Evaluate`, screenshots, or a raw CDP handle. Those would leak the engine into
plugin code and make the allowlist unenforceable.

`Open`, `NewPage`, `Goto`, `WaitFor`, `Content`, and `Get` all require a plugin identity
on the context (stamped by the scoped facade) and call `policy.Gate.CheckWork` before
doing work. A cancelled context does not leave a Chromium context behind: `Session.Close`
and plugin disable both close the underlying browser context.

```go
sess, err := h.Browser().Open(ctx, browser.OpenOptions{
    AllowedHosts: []string{"example-market.test", "cdn.example-market.test"},
})
if err != nil {
    return err
}
defer sess.Close(ctx)

page, err := sess.NewPage(ctx)
if err != nil {
    return err
}
defer page.Close(ctx)

if err := page.Goto(ctx, auctionURL); err != nil {
    return err
}
if err := page.WaitFor(ctx, "article.lot", 15*time.Second); err != nil {
    return err
}
html, err := page.Content(ctx)
```

Two errors a plugin must handle: `policy.ErrPluginDisabled` and `browser.ErrDenied`.
Disabled means stop. Denied means the URL failed the allowlist or the private-network
check — fix the URL, do not retry the same one.

---

## Why this is a capability

Principle 1 still holds for ordinary libraries. Browser is different: it *is* the
network. If the plugin launches Chromium, disable can cancel a `context.Context` and the
process keeps crawling. The host has to own the process so disable can close it.

The same ownership gives the host a single SSRF checkpoint. A compiled allowlist inside
one plugin is a convention; an interceptor in this module is the actual control.

CI's plugin dependency test rejects `playwright`, `chromedp`, `rod`, and equivalent
automation libraries in any plugin module. The architectural test is the rule, not a
review comment.

Plugins may still use `net/http` for ordinary APIs. That hole is already documented for
the in-process trusted model. Browser is the path for JavaScript-rendered pages and for
fetches that must share a cookie jar and an allowlist.

---

## URL and network policy

Every URL the engine would touch — the argument to `Goto`/`Get`, every redirect, every
subresource — is checked before the request leaves the host:

| Rule | Why |
|---|---|
| Scheme `https` only | Cleartext and `file:`, `javascript:`, `data:` are not fetch targets. Inline `data:` / `blob:` in HTML are not network fetches and are not gated. |
| Host is in `AllowedHosts` | Plugin-supplied allowlist; the host does not hard-code any site. |
| No userinfo, no IP literals, no brackets | Stops `https://user@evil/` and literal-address bypasses. |
| Port omitted or `443` | Alternate ports are a common allowlist escape. |
| DNS result is not private, link-local, loopback, or CGNAT | Stops DNS rebinding onto the host's own network. Rechecked at connect time. |

`AllowedHosts` entries are lowercase DNS names (`example.test`), not URLs. International
names are stored as A-labels. An empty list is `ErrInvalidAllowlist` at `Open`, not an
unrestricted session.

The interceptor is fail-closed: a URL the checker cannot classify is denied.

---

## Lifecycle and the kill switch

`Open` admits a session: `CheckWork`, then create a browser context owned by this module,
attributed to the plugin and, when present, the current job. The session is registered
so disable can find it.

Disable:

1. rejects new `Open` / `NewPage` / `Goto` / `Get` / `WaitFor` / `Content` with
   `ErrPluginDisabled` before any engine call
2. cancels the session's context
3. closes every page and the browser context; in-flight navigations and fetches fail

Unlike AI, the host can actually stop this work — it owns the process. An admitted
navigation is not promised to finish. `Close` is idempotent.

`pluginhost` teardown also closes sessions for that plugin, so a `Shutdown` timeout
cannot leave Chromium running under a disabled id.

---

## Limits

Finite, host-wide, and not configurable by plugin code:

| Limit | Default | Notes |
|---|---|---|
| Sessions per plugin | 1 | Collection is sequential; raise later if a second plugin needs overlap. |
| Pages per session | 4 | Extra `NewPage` returns `ErrLimit`. |
| Document HTML (`Content`) | 5 MiB | Larger pages fail the call; the session stays up. |
| Resource body (`Get`) | 10 MiB | Same as BIDRL's per-image cap, enforced here so every plugin inherits it. |
| Navigation timeout | 30s | `WaitFor` uses the caller's duration, capped at 60s. |

The engine is one Chromium (or equivalent) for the process. Host-wide page count is
capped so a stuck plugin cannot spawn unbounded renderers. Cookies, storage, and cache
are scoped to the session and discarded on `Close` or disable. V1 has no persistent
browser profile and no credential-backed login jar.

---

## Fake backend

Tests and local development use an in-process `Fake` that never launches Chromium and
never opens a real socket. The test supplies an `http.Handler` and the DNS names that map
to it; `Goto("https://hello.test/…")` is allowlist-checked and then served from that
handler. `hello` and the module tests use `Fake`. Fake still honours disable and context
cancel.

## Production engine

Host config `browser.engine: playwright` launches one Chromium for the process. Sessions
are isolated browser contexts; cookies, storage, and cache die with `Close`. Every
navigation, redirect, subresource, and `Get` still goes through the allowlist interceptor.
At connect time the engine resolves DNS and rejects private, loopback, link-local, and
CGNAT addresses — there is no loopback or `http://` exception. Missing Chromium is
installed on first start (`chromium` only), or ahead of time with
`go run github.com/mxschmitt/playwright-go/cmd/playwright@v0.6201.1 install chromium`.
Plugins never select this; CI still rejects a plugin that imports Playwright.

---

## Events emitted

Browser work is chatty. Do not emit per-navigation events.

| Event | When |
|---|---|
| `core.browser.denied` | a URL failed allowlist or private-network checks; payload includes plugin, job when present, URL host, and reason |

Denied is the audit row. Ordinary `Open`/`Close` are logs tagged with plugin and job.

---

## Errors

```go
var (
    ErrInvalidAllowlist = errors.New("browser: allowlist is empty or invalid")
    ErrDenied           = errors.New("browser: url denied")
    ErrLimit            = errors.New("browser: session or page limit")
    ErrEngine           = errors.New("browser: engine failed")
)
```

`policy.ErrPluginDisabled` is returned as-is from admission. `ErrDenied` wraps the
reason (scheme, host, port, userinfo, address); callers use `errors.Is`.

---

## What is deliberately missing

| Missing | Why |
|---|---|
| `Evaluate` / CDP / screenshots | Engine leak; allowlist would not see scripted fetches the same way. Parse `Content`. |
| Persistent profiles, saved cookies | A login jar is a credential. If a site needs auth, that is a later capability, not a cookie file in the plugin. |
| Host-hardcoded site lists | The plugin names the hosts it intends to touch; the host enforces them. |
| A notifications-style admin UI in v1 | Denied events and logs are enough until a second operator needs a session list. |
