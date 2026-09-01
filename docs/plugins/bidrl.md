# Plugin: `bidrl`

The first real plugin. Sketched to validate the host boundary — not a specification.

**What it does.** Analyses BIDRL auction lots from their photographs rather than their
titles, because BIDRL's own listings warn that titles may not describe the item, and
surfaces lots that are anomalously cheap relative to what the pictures actually show.

---

## Shape

```
plugins/bidrl/
  plugin.go        manifest, wiring
  collect/         host.Browser(): auction enumeration, lot pages, images
  analyze/         one vision call per lot, all photos together, schema-enforced
  price/           grounded pricing with stored citations
  score/           deal score + mislabel score
  store/           migrations, queries

web/src/plugins/bidrl/
  index.tsx        nav, routes, icons, feed, auction view, lot detail
```

## Pipeline

```
selected auction
      ↓
  collect        enumerate lots, cache images as blobs
      ↓
  analyze        one AI().Chat per lot, all photos, structured output
      ↓
   ┌──────────────┬──────────────────────┐
   │ SKIP         │ RESEARCH             │
   │ generic,     │ branded, model number│
   │ commodity    │ visible, unusual     │
   └──────────────┴──────────┬───────────┘
                             ↓
                          price        grounded AI call with citations
                             ↓
                          score        deal score + mislabel score
                             ↓
                        ranked feed
```

---

## Decisions carried from design

**Collection goes through `Host.Browser()`, not a plugin-owned engine.** Playwright
(or anything else) is an implementation detail of [`browser`](../modules/browser.md).
The plugin passes the BIDRL/CDN DNS names it intends to touch; the host enforces HTTPS,
the allowlist, private-network rejection, and teardown on disable. CI rejects a plugin
that imports an automation library.

**Collection requires the playwright engine.** The item feed only exists because the
page's JavaScript asks for it, and the `fake` engine runs none: it answers from
in-process fixtures that know one test auction. Under `fake`, every real auction
collects zero lots. The Docker image sets `CC_BROWSER_ENGINE=playwright`; a local run
needs `browser.engine: playwright` in config or that variable in the environment.

**Lots come from the gallery's own item feed, not its HTML.** The BIDRL bid gallery is
an AngularJS app: the served markup carries no lot links at all. Collection opens
`/bidgallery/perpage_100/page_N/` and reads the `/api/getitems` responses the app posts,
which the browser session already captures. That feed carries the canonical lot URL,
title, lot number, current bid, and full-size image URLs, so a lot needs no page visit of
its own. Enumeration probes the gallery once first; when the markup does contain lot
links — a print catalog, or a server-rendered page — it scrapes those instead, and the
scrape also remains the fallback when no feed arrives.

**Every v1 action is user-triggered.** Collection starts only from "add auction" or
"collect" on a search/SITES hit, a scan starts only from "scan", search starts only from
"search", SITES listing starts only from "refresh list" or as part of a search, pricing
starts only inside that requested scan or from "reprice", and bids refresh only from
"refresh bids". There is no cron or event-triggered work, and the backend manifest sets
`Automated: false`. Jobs still make each requested operation durable, cancellable,
budgeted, and subject to the host-capability kill switch.

Images and analyses are cached until the user deletes the auction; identification does
not rerun during a bid refresh. Deletion removes the auction's records and blobs. The
storage module's finite per-plugin quota applies, and collection stops visibly rather than
evicting audit evidence when the quota is full. This is both the sensible engineering
choice and the respectful one: BIDRL's user agreement prohibits automated processes that
monitor or copy its pages, so explicit user actions with aggressive caching replace
continuous crawling.

**Browser targets use a fixed HTTPS host allowlist.** The plugin accepts BIDRL auction and
lot URLs only when their parsed host is in the compiled allowlist and their scheme is
`https`. It passes that list to `Browser().Open`. The host applies the same rule to
redirects and subresources. Userinfo, alternate ports, IP literals, and all other origins
are rejected. This prevents supplied URLs from turning a browser session into an SSRF
primitive.

**Identification basis gates valuation.** The vision call must report *why* it identified
something:

| Basis | May assert a price? |
|---|---|
| `exact_text` — model number legible in a photo | yes |
| `barcode` / SKU | yes |
| `distinctive_visual_match` | **no** — goes to a "worth opening" bucket, no number |
| `product_family` | no |
| `category_only` | discarded |

This is the difference between a tool still trusted in month three and a feed of confident
fiction. A mesh chair confidently valued at $450 because it resembles an Aeron is the
failure mode that kills the whole thing.

**Vision and pricing are separate calls.** The vision model says what it sees; a pricing
call uses `GroundingOptions` and returns typed citations. One call doing both produces
invented MSRPs. The plugin resolves each citation's text offsets and source index, then
stores the cited text, source URL and title, source publication time when available, and
retrieval time with the valuation. The UI displays that evidence beside the estimate.

**Resolution is the cost lever.** Medium for every photo; high on retry for a label or
plate the model could not read.

**Price the minority.** Expect 10–30% of lots to survive the first pass. That is where the
intelligence budget goes, and where the genuine difficulty lives — making "current market
price" trustworthy enough that a $500 estimate means about $500 today.

---

## What it exercises in the host

This is why it is the right first real plugin: it touches nearly the whole surface.

| Host capability | Use |
|---|---|
| `Browser()` allowlisted sessions | collection: auction pages, lot pages, images |
| `AI()` vision, multi-image, structured output | the analyze stage |
| `AI()` grounding options and citations | the price stage |
| `Jobs()` enqueue-only, long-running with progress | user-triggered collection, scans, pricing, bid refreshes, search, and SITES discovery |
| `Events()` | `bidrl.deal_found`, `bidrl.lot.analyzed`, `bidrl.alert` |
| `Store()` / `Blobs()` | lots, analyses, cached photos |
| Budgets + host-capability kill switch | durable user-triggered work is still admitted, reserved, and cancellable |
| UI | a feed with filters, an auction view, a lot detail |

If this can be built without punching a hole through the `Host` facade, the boundary is
right.

---

## Plugin surface

| Surface | Contract |
|---|---|
| Jobs | `collect`, `scan`, `reprice`, `refresh`, `search`, `discover` — enqueue-only, concurrency 1, two-hour timeout |
| API | `GET/POST /api/plugins/bidrl/auctions`, `GET/DELETE /auctions/{id}`, `POST /auctions/{id}/scan`, `POST /auctions/{id}/refresh` |
| API | `GET /api/plugins/bidrl/lots/{id}`, `POST /lots/{id}/reprice`, `GET /feed?filter=` |
| API | `POST/GET /search`, `GET /sites/auctions`, `POST /sites/refresh` |
| Events | `bidrl.auction.collected`, `bidrl.lot.analyzed`, `bidrl.lot.priced`, `bidrl.deal_found`, `bidrl.scan.completed`, `bidrl.bids.refreshed`, `bidrl.search.completed`, `bidrl.sites.discovered` |
| UI | `/bidrl` feed + search, `/bidrl/auction/:id`, `/bidrl/lot/:id` |

Allowlisted hosts: `www.bidrl.com`, `bidrl.com`, `d3ugkdpeq35ojy.cloudfront.net`. The fake
browser serves a canned three-lot warehouse auction at
`https://www.bidrl.com/auction/42/bidgallery`. Configure `cheap-vision` (chat+vision) and
`grounded-price` (chat+grounding) routes before scanning.

---

## Search

Search is user-triggered. It does not crawl BidRL on a schedule.

A query is expanded into a few BidRL keywords (the full phrase plus distinctive model
tokens such as `K-Supreme` or `20V`). Live hits come from BidRL's own `/allitems`
keyword page, captured through `Host.Browser()` the same way collection captures the
gallery feed.

**SITES locations rank first.** BidRL's location menu — the same list as
[Turlock](https://www.bidrl.com/affiliate/turlock-19/) — is the SITES dealer family.
Search reads that menu, loads each affiliate landing page, and treats those open auctions
as preferred. Scope `prefer` (default) still shows other BidRL lots below them; `only`
hides the rest; `all` ignores location. Plugin config `preferredAffiliateIds` can narrow
the family (empty means the whole live menu).

**Titles still lie.** After a scan, search also matches the vision identification and
model number, so a chair titled "office mesh" still rises for "herman miller" if the
photos said so.

"Refresh list" enumerates currently open SITES auctions without searching, so a warehouse
can be collected from the list instead of a pasted URL.

---

## Feed

The treasure-hunting feed is still the default reading, now with a search box above it:

| Filter | Means |
|---|---|
| Best deals | priced, large gap between bid and market |
| Likely mislabeled | high disagreement between title and photos |
| Model number found | `exact_text` basis, highest confidence tier |
| Worth opening | visually interesting, deliberately unpriced |

Every row shows the BIDRL title beside what the photos suggest, and links back to the lot.

---

## Acceptance criteria

### Pricing evidence

- A numeric valuation is stored only for `exact_text` or `barcode` identification and only
  when the grounded response includes at least one citation that shows the matching model
  or code, a price and currency, item condition, source URL, and retrieval time.
- Missing or mismatched evidence leaves the lot unpriced. The feed shows the source link,
  quote, retrieval age, and whether the evidence is an asking or sold price; it never
  presents an uncited model estimate as market price.
- Repricing creates a new evidence record rather than overwriting the prior one, so a
  displayed valuation can be audited against the evidence used at that time.

### Resource limits

- Collection rejects more than 500 lots per auction, more than 12 images per lot, an image
  over 10 MiB, or more than 100 MiB of images for one lot; rejected items are recorded with
  a visible reason.
- Scan, reprice, bid-refresh, search, and SITES-discover job definitions are enqueue-only, have concurrency `1`
  per operation, and time out after two hours. The scan performs at most four concurrent
  AI calls and stops admitting calls when its context is cancelled or budget reservation
  fails.
- Disabling BIDRL makes new plugin HTTP requests return `503`, blocks new jobs, AI
  dispatches, and browser sessions, closes admitted browser sessions, and cancels running
  job contexts. A paid call admitted before disable may finish; its usage is stored and
  its budget reservation is settled.
