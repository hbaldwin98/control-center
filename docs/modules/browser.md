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

    // Subscribe opens a read-only websocket to an allowlisted wss:// URL and streams
    // the text frames the server sends. Every frame the host will write is declared
    // here, before the socket opens; there is no send channel afterwards.
    Subscribe(ctx context.Context, url string, opts SubscribeOptions) (Subscription, error)

    Close(ctx context.Context) error
}

type SubscribeOptions struct {
    // Handshake frames are written once, in order, as soon as the socket opens. Each
    // is a JSON value under 4 KiB; the host does not interpret them.
    Handshake []json.RawMessage
    // KeepAlive answers a received frame whose top-level "event" equals Event by
    // writing Reply, so an application heartbeat needs no send channel.
    KeepAlive []KeepAliveRule
}

type KeepAliveRule struct {
    Event string
    Reply json.RawMessage
}

type Subscription interface {
    // Frames closes when the connection ends -- by Close, by disable, by session
    // teardown, or by the server. Err then says why.
    Frames() <-chan Frame
    Err() error
    Close(ctx context.Context) error
}

type Frame struct {
    At   time.Time
    Data []byte
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

    // Post sends application/x-www-form-urlencoded to an allowlisted URL. Same
    // cookie jar, SSRF checks, and size bound as Get. Form is the only body.
    Post(ctx context.Context, url string, form url.Values) (Resource, error)

    // Responses are JSON/CSV bodies the page fetched (XHR/fetch), already
    // allowlisted. An SPA that POSTs usage to its API with a bearer token in
    // localStorage shows up here; Get cannot replay that. Bounded by max-resource.
    Responses(ctx context.Context) ([]Resource, error)

    // Fill sets the value of the first matching control. It does not run script.
    Fill(ctx context.Context, selector, value string) error

    // Click the first matching element. A navigation that follows is allowlist-checked
    // the same way as Goto.
    Click(ctx context.Context, selector string) error

    Close(ctx context.Context) error
}

type Resource struct {
    URL    string
    MIME   string
    Body   []byte
    Status int
}
```

Conversation and DOM state belong to the `Page`. The plugin parses HTML and captured
XHR bodies itself; v1 does not expose `Evaluate`, screenshots, or a raw CDP handle.
Those would leak the engine into plugin code and make the allowlist unenforceable.

`Open`, `NewPage`, `Goto`, `WaitFor`, `Content`, `Get`, `Post`, `Responses`, `Fill`, and `Click` all require a plugin identity
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
| Scheme `https` only, or `wss` for `Subscribe` | Cleartext and `file:`, `javascript:`, `data:` are not fetch targets. Inline `data:` / `blob:` in HTML are not network fetches and are not gated. A fetch scheme is not a feed scheme: each call accepts exactly one. |
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

1. rejects new `Open` / `NewPage` / `Subscribe` / `Goto` / `Get` / `WaitFor` / `Content`
   with `ErrPluginDisabled` before any engine call
2. cancels the session's context
3. closes every page, every subscription, and the browser context; in-flight
   navigations and fetches fail, and open feeds close their `Frames` channel

Unlike AI, the host can actually stop this work — it owns the process. An admitted
navigation is not promised to finish. `Close` is idempotent.

`pluginhost` teardown also closes sessions for that plugin, so a `Shutdown` timeout
cannot leave Chromium running under a disabled id.

---

## Limits

Finite, host-wide, and not configurable by plugin code:

| Limit | Default | Notes |
|---|---|---|
| Sessions per plugin | 2 | Collection is sequential, but a plugin may hold a long-lived session for a realtime feed and still need one for work. |
| Pages per session | 4 | Extra `NewPage` returns `ErrLimit`. |
| Document HTML (`Content`) | 5 MiB | Larger pages fail the call; the session stays up. |
| Resource body (`Get` / `Responses`) | 10 MiB | Same as BIDRL's per-image cap, enforced here so every plugin inherits it. `Responses` keeps the last 32 capturable XHR/fetch bodies. |
| Navigation timeout | 30s | `WaitFor` uses the caller's duration, capped at 60s. |
| Subscriptions per session | 4 | A multiplexed protocol joins many channels over one socket, so this is rarely the binding limit. |
| Handshake | 512 frames, 4 KiB each, 256 KiB total | One small frame per channel, so an auction's worth of lots fits in one subscription. |
| Frame body | 1 MiB | Larger frames end the subscription. 256 frames are buffered; a plugin that stops draining loses frames, counted and logged, rather than growing the heap. |

The engine is one Chromium (or equivalent) for the process. Host-wide page count is
capped so a stuck plugin cannot spawn unbounded renderers. Cookies, storage, and cache
are scoped to the session and discarded on `Close` or disable. V1 has no persistent
browser profile. A site that needs a password is logged into each session with `Fill`
and the host-only `FillCredential` on the plugin-facing page (the host reads the
credential and types it; the plugin never receives the secret). There is still no
saved cookie jar.

---

## Realtime feeds

`Get` and `Responses` answer "what is the price now". A site that pushes changes over a
websocket -- a bid gallery updating every card as bids land -- answers "what changed",
and polling it lot by lot is both slower and more load on the origin than listening.

`Subscribe` is that path, and it is deliberately not a socket:

- **The write side is declared, not held.** The plugin passes the handshake frames and
  the heartbeat replies before the connection opens. The host writes those and nothing
  else. This is the same line `Post` draws by taking a form instead of a body: a plugin
  that could write arbitrary frames after connect would make the allowlist stop being
  the whole story about what a session can do.
- **The dial does not go through the engine.** No Chromium, no page cookie jar. A
  subscription reaches one allowlisted host with no ambient credentials, which also
  means it can only join channels that need no authenticated handshake -- a private
  channel whose token is derived from the connection is out of reach by construction.
- **The same connect-time gate applies.** The handshake is an ordinary HTTPS request,
  so the dialer resolves the name and refuses private, loopback, link-local, and CGNAT
  addresses exactly as the CONNECT proxy does for Chromium. Port 443 only.

```go
sub, err := sess.Subscribe(ctx, "wss://feed.example.test/app/key?protocol=7", browser.SubscribeOptions{
    Handshake: []json.RawMessage{[]byte(`{"event":"subscribe","data":{"channel":"lot-1001"}}`)},
    KeepAlive: []browser.KeepAliveRule{{Event: "ping", Reply: []byte(`{"event":"pong","data":{}}`)}},
})
if err != nil {
    return err
}
defer sub.Close(ctx)

for frame := range sub.Frames() {
    apply(frame.Data)
}
return sub.Err()
```

A feed is a latency improvement, never the record. It can drop frames behind a slow
reader, miss the window before it connected, and end whenever the server decides to.
A plugin that listens still polls on a schedule to reconcile; BIDRL narrows the window
between refreshes with it, it does not replace `refresh`.

A subscription is usually not job work. It lasts as long as someone is watching, not as
long as a task takes, so BIDRL holds one from an ordinary route for the life of an SSE
request: the socket closes when the viewer leaves, with no window, no renewal, and no
durable row for work nobody would resume.

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
| `Evaluate` / CDP / screenshots | Engine leak; allowlist would not see scripted fetches the same way. Parse `Content` and `Responses`. |
| Persistent profiles, saved cookies | A login jar is a credential store. Login each session with `Fill` / `FillCredential` / `Click`; cookies die with the session. |
| Host-hardcoded site lists | The plugin names the hosts it intends to touch; the host enforces them. |
| A notifications-style admin UI in v1 | Denied events and logs are enough until a second operator needs a session list. |
