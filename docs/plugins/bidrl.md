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
  collect.go       gallery ID census, then paced ItemData POSTs and photo Gets
  itemdata.go      parse POST /api/ItemData
  pusher.go        parse GET /aucbeat/pusher/{auction}-{item}.json
  pace.go          400ms origin spacing, 429/403 backoff
  analyze.go       one vision call per lot, category + search_terms
  price.go         grounded pricing with stored citations
  jobs.go          collect, scan, reprice, refresh, enrich, search, discover

web/src/plugins/bidrl/
  index.tsx        one nav item; Feed / Auctions / Lots tabs; auction and lot views
```

## Pipeline

```
selected auction
      ↓
  collect        gallery IDs → ItemData + photo blobs
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

**The gallery is an ID census, not the collect record.** The BIDRL bid gallery is an
AngularJS app: the served markup carries no lot links. Collection opens
`/bidgallery/perpage_100/page_N/` and reads `/api/getitems` (or scrapes lot links from a
print catalog / server-rendered page when that feed never arrives) only to learn each
lot's numeric `item_id`. It then POSTs `/api/ItemData` (`item_id` + `auction_id` form
body) once per lot through `Page.Post`, and that JSON is the stored title, description,
photos, bids, end time, and auction. Photo URLs are fetched with `Page.Get` into blobs.
When ItemData succeeds, collection never `Goto`s the lot HTML page.

**Live bids come from pusher, not a second ItemData pass.** "Refresh bids" GETs
`/aucbeat/pusher/{auction_id}-{item_id}.json` — a tiny snapshot of current bid, minimum,
increment, high bidder, bid count, end time, reserve, and whether bidding was extended.
The UI countdown is local from the stored `ends_at`; it does not poll BidRL. Optional
`POST /lots/{id}/enrich` re-POSTs ItemData for one lot when the operator wants a fuller
record again.

**Origin requests are paced.** ItemData POSTs and pusher GETs share one in-flight slot
and a 400ms minimum interval. HTTP 429 or 403 backs off; three consecutive such responses
stop the job rather than continuing into a ban.

**Every v1 action is user-triggered.** Collection starts only from "add auction" or
"collect" on a search/SITES hit, a scan starts only from "scan", search starts only from
"search", SITES listing starts only from "refresh list" or as part of a search, pricing
starts only inside that requested scan or from "reprice", bids refresh only from
"refresh bids", and a single-lot ItemData refresh only from "enrich". There is no cron
or event-triggered work, and the backend manifest sets
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

**Vision and pricing are separate calls.** The vision model says what it sees. Pricing
asks `Host.Search()` for public listings of that model, **eBay sold comps first**, then
retail, then other resale marketplaces, then the open web. A chat call (no provider web-search)
picks a dollar amount already written in one of those hits. Invented MSRPs are rejected:
`price_cents` must appear as `$…` in the hit title or snippet, and the stored quote is
that snippet, not the model's prose. The feed shows the site, asking vs sold, quote, and
link.

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
| `Browser()` allowlisted sessions | gallery census, `Post` ItemData, `Get` pusher snapshots and photos |
| `Search()` host-owned web lookup | eBay sold comps first, then retail, then other resale |
| `AI()` vision, multi-image, structured output | the analyze stage |
| `AI()` chat with a cited-price schema | pick a `$` amount already written in those hits |
| `Jobs()` enqueue-only, long-running with progress | user-triggered collection, scans, pricing, bid refreshes, enrich, search, and SITES discovery |
| `Events()` | `bidrl.deal_found`, `bidrl.lot.analyzed`, `bidrl.lot.enriched` |
| `Store()` / `Blobs()` | lots, analyses, cached photos |
| Budgets + host-capability kill switch | durable user-triggered work is still admitted, reserved, and cancellable |
| UI | Feed, Auctions, Lots catalog, auction and lot views |

If this can be built without punching a hole through the `Host` facade, the boundary is
right.

---

## Plugin surface

| Surface | Contract |
|---|---|
| Jobs | `collect`, `scan`, `reprice`, `refresh`, `enrich`, `search`, `discover` — enqueue-only, concurrency 1, two-hour timeout |
| API | `GET/POST /api/plugins/bidrl/auctions`, `GET/DELETE /auctions/{id}`, `POST /auctions/{id}/scan`, `POST /auctions/{id}/refresh` |
| API | `GET /lots?q=&bucket=&category=&ending=soon`, `GET /lots/{id}`, `POST /lots/{id}/reprice`, `POST /lots/{id}/enrich`, `GET /feed?filter=` |
| API | `POST/GET /search`, `GET /sites/auctions`, `POST /sites/refresh` |
| Events | `bidrl.auction.collected`, `bidrl.lot.analyzed`, `bidrl.lot.priced`, `bidrl.lot.enriched`, `bidrl.deal_found`, `bidrl.scan.completed`, `bidrl.bids.refreshed`, `bidrl.search.completed`, `bidrl.sites.discovered` |
| UI | `/bidrl` feed + search, `/bidrl/auctions`, `/bidrl/lots`, `/bidrl/auction/:id`, `/bidrl/lot/:id` |

Allowlisted hosts: `www.bidrl.com`, `bidrl.com`, `d3ugkdpeq35ojy.cloudfront.net`. The fake
browser serves a canned three-lot warehouse auction at
`https://www.bidrl.com/auction/42/bidgallery`, plus `POST /api/ItemData` and
`GET /aucbeat/pusher/` fixtures. Before scanning, connect a provider and
assign models to the plugin's two declared routes, `cheap-vision` (chat+vision) and
`grounded-price` (chat). The plugin screen lists both by purpose; Models will
too. Do not invent other names — the plugin asks for these two.

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

**Titles still lie.** After a scan, search also matches the vision identification, model
number, category, and search terms, so a chair titled "office mesh" still rises for
"herman miller" if the photos said so. Vision categories are tools, furniture,
electronics, appliances, outdoor, automotive, sporting, household, collectibles, or other.

"Refresh list" enumerates currently open SITES auctions without searching, so a warehouse
can be collected from the list instead of a pasted URL.

---

## Feed

The treasure-hunting feed lives at `/bidrl` with search above it. Auctions and the lot
catalog have their own tabs so collecting and browsing do not share one dumped page.

| Filter | Means |
|---|---|
| Best deals | priced, large gap between bid and the comparable (eBay first) |
| Likely mislabeled | high disagreement between title and photos |
| Model number found | `exact_text` basis, highest confidence tier |
| Worth opening | visually interesting, deliberately unpriced |

Every row shows a thumb, the BidRL title beside what the photos suggest, the current bid,
a local countdown from stored `ends_at`, category, and a link back to BidRL.

`/bidrl/auctions` is SITES discovery, paste-a-URL collect, and already-collected auctions.
`/bidrl/lots` is the catalog: local text, bucket, category, and ending-soon. Auction and
lot views add a bidder card (high bidder, bid count, min bid, reserve, extended) and
Open on BidRL.

---

## Acceptance criteria

### Pricing evidence

- A numeric valuation is stored only for `exact_text` or `barcode` identification, and only
  from the first search tier that returns a hit naming the model and a dollar amount:
  eBay, then retail, then other resale marketplaces, then the open web. The model's
  `price_cents` must match a `$` amount in that hit.
- Missing or mismatched evidence leaves the lot unpriced. The feed shows the source site,
  quote, retrieval age, and whether the listing is asking or sold. It never presents an
  uncited model estimate as a market price.
- Repricing creates a new evidence record rather than overwriting the prior one, so a
  displayed valuation can be audited against the evidence used at that time.

### Resource limits

- Collection rejects more than 500 lots per auction, more than 12 images per lot, an image
  over 10 MiB, or more than 100 MiB of images for one lot; rejected items are recorded with
  a visible reason.
- Scan, reprice, bid-refresh, enrich, search, and SITES-discover job definitions are enqueue-only, have concurrency `1`
  per operation, and time out after two hours. The scan performs at most four concurrent
  AI calls and stops admitting calls when its context is cancelled or budget reservation
  fails.
- ItemData and pusher traffic to BidRL is paced at 400ms with one request in flight. Three
  consecutive HTTP 429 or 403 responses stop the job.
- Disabling BIDRL makes new plugin HTTP requests return `503`, blocks new jobs, AI
  dispatches, and browser sessions, closes admitted browser sessions, and cancels running
  job contexts. A paid call admitted before disable may finish; its usage is stored and
  its budget reservation is settled.
