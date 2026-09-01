# search

**Layer 3** · `internal/core/search` · imports `policy` ·
used by `pluginhost`

**Responsibility.** Own web lookup for plugins: admit the query, call a host-owned
engine, and return public HTTPS hits. Plugins never choose SearXNG or see its URL.

This is a host capability even with one consumer. A plugin-owned HTTP client to a
search engine is a network the kill switch cannot close, and result URLs need the same
public-HTTPS filter as [`browser`](browser.md).

---

## Interface

```go
package search

type Search interface {
    Query(ctx context.Context, req Request) ([]Hit, error)
}

type Request struct {
    Query          string
    MaxResults     int      // zero uses the host default (4); hard cap 8
    AllowedDomains []string // empty means any public HTTPS host; a name keeps that host and its subdomains
}

type Hit struct {
    URL     string
    Title   string
    Snippet string
}
```

`Query` requires a plugin identity on the context (stamped by the scoped facade) and
calls `policy.Gate.CheckWork` before contacting the engine. Disable rejects new queries.
An admitted request is cooperative: its context is cancelled.

---

## Engines

| Engine | When |
|---|---|
| `fake` | Tests and local runs without a sidecar. In-process fixtures for a few model strings. |
| `searxng` | Docker Compose sidecar. The host GETs `/search?format=json`. JSON format is enabled only on that private instance. |

Config (`search.engine`, `search.searxng.url`) and env (`CC_SEARCH_ENGINE`,
`CC_SEARCH_SEARXNG_URL`) select the engine. Plugins cannot pass a base URL.

The SearXNG URL may be plain HTTP on the Docker network. **Result** URLs must be
`https`, DNS names (no IP literals), port 443 or omitted, and optionally on
`AllowedDomains`. A listed domain keeps that host and its subdomains (`ebay.com` keeps
`www.ebay.com`). Private, loopback, and `http:` results are dropped. When an allowlist
is set, the engine is asked for up to eight hits so filtering still has material.

---

## What this is not

It is not the provider's web-search add-on. BidRL looks up a model number here, eBay
sold listings first, then a chat call (no `Grounding`) picks a dollar amount already
written in those hits. The model still costs tokens; the search queries do not.

It is not Playwright pointed at Google. Headless search-engine crawls fail closed and
do not belong in a plugin.

---

## Docker

`docker compose up --build` starts `searxng` on the internal network only (no host
port) and sets Control Center to `CC_SEARCH_ENGINE=searxng` against
`http://searxng:8080`. Settings live in `deploy/searxng/` (`settings.yml` plus
`limiter.toml`). The instance keeps DuckDuckGo, Brave, Wikipedia, and Wikidata —
not Google, Bing, or Tor onion engines, which CAPTCHA or fail to register from a
datacenter IP. Recreate the sidecar after changing those files:

`docker compose up -d --force-recreate searxng`
