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
  jobs.go          collect, scan, reprice, refresh, enrich, search, intent, discover
  intent.go        user-triggered intent match over collected lots
  embeddings.go    SQLite-stored lot vectors; cosine rank at Ask time

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

**Everything is user-triggered except two scheduled ticks, and those default to off.**
Collection starts from "add auction" or "collect", a scan from "scan", search from
"search", intent matching from "Ask", SITES listing from "refresh list", pricing from that
scan or from "reprice", bids from "refresh bids", a single-lot ItemData refresh from
"enrich", expired-record deletion from "Remove ended", and a watchlist from "Run now".

The exceptions are `sweep` and `match` — see [automation](#automation). Both check
`automation.enabled` before doing anything, and it is `false` by default, so a fresh
install still touches BidRL only when a person asks. Because scheduled work exists at all,
the manifest sets `Automated: true`: the honest reading is that this plugin *can* start
work without a person, and the per-plugin budget that flag forces is the point of
declaring it. Jobs still make every operation durable, cancellable, budgeted, and subject
to the host-capability kill switch.

Images and analyses are cached until the user deletes the auction or removes ended
records; identification does not rerun during a bid refresh. Deletion removes the
auction's records and blobs. **Expiry does nothing on its own.** The stored `ends_at`
drives a local countdown; once it passes, the lot or auction still sits in the feed,
catalog, and collected list. There is no cron and no BidRL poll. "Remove ended" on
`/bidrl/auctions` deletes auctions whose close (or every dated lot) is in the past,
individual ended lots from auctions that are still open, and stale SITES listings —
photos, analyses, and comparables included. Lots with no end time are left alone. The
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
asks `Host.Search()` for public listings of that model **once**, ranks hits **eBay
first, then retail, then other resale**, then the open web. A chat call (no provider web-search)
picks a dollar amount already written in one of those hits. Invented MSRPs are rejected:
`price_cents` must appear as `$…` in the hit title or snippet, and the stored quote is
that snippet, not the model's prose. The feed shows the site, asking vs sold, quote, and
link.

**Resolution is the cost lever.** Medium for every photo; high on retry for a label or
plate the model could not read.

**Price the minority.** Expect 10–30% of lots to survive the first pass. Identical
model numbers reuse a comparable looked up in the last seven days, so a pallet of the
same Keurig does not hit SearXNG once per lot. Reprice, a stale lookup, or a clear
condition split (working vs for-parts) does a new search.

---

## What it exercises in the host

This is why it is the right first real plugin: it touches nearly the whole surface.

| Host capability | Use |
|---|---|
| `Browser()` allowlisted sessions | gallery census, `Post` ItemData, `Get` pusher snapshots and photos |
| `Search()` host-owned web lookup | one SearXNG lookup per lot, ranked eBay → retail → other resale |
| `AI()` vision, multi-image, structured output | the analyze stage |
| `AI()` chat with a cited-price schema | pick a `$` amount already written in those hits |
| `AI()` chat to expand an intent into related gear words | one `intent-expand` call; tent/headlamp/lantern, not only "camping" |
| `AI()` Embed over titles (and identifications, if already scanned) | embed the expanded query plus cached lot vectors; cosine rank locally |
| `Jobs()` enqueue-only, long-running with progress | user-triggered collection, scans, pricing, bid refreshes, enrich, search, intent, and SITES discovery |
| `Events()` | `bidrl.deal_found`, `bidrl.lot.analyzed`, `bidrl.lot.enriched` |
| `Store()` / `Blobs()` | lots, analyses, cached photos |
| Budgets + host-capability kill switch | durable user-triggered work is still admitted, reserved, and cancellable |
| UI | Overview, Auctions, Lots catalog, Intent, auction and lot views |

If this can be built without punching a hole through the `Host` facade, the boundary is
right.

---

## Plugin surface

| Surface | Contract |
|---|---|
| Jobs | `collect`, `scan`, `reprice`, `refresh`, `enrich`, `search`, `intent`, `discover`, `watch` — enqueue-only, concurrency 1, two-hour timeout |
| Jobs | `sweep` (`0 */6 * * *`) and `match` (`30 */6 * * *`) — the only scheduled ones, both inert while `automation.enabled` is false |
| API | `GET/POST /api/plugins/bidrl/auctions`, `GET/DELETE /auctions/{id}`, `POST /auctions/{id}/scan`, `POST /auctions/{id}/refresh` |
| API | `POST /cleanup` — remove ended auctions, leftover ended lots, and ended SITES listings; never a saved lot |
| API | `POST/DELETE /lots/{id}/favorite`, `GET /favorites?q=&category=&affiliate=` |
| API | `GET/POST /watchlists`, `PATCH/DELETE /watchlists/{id}`, `POST /watchlists/{id}/run` |
| API | `GET /findings?state=&watchlist=`, `POST /findings/{id}/accept`, `POST /findings/{id}/reject` |
| API | `GET /automation`, `POST /automation/resume` — schedule state and clearing the throttle latch |
| API | `GET /locations` — the SITES locations you have lots at, with lot counts |
| API | `GET /lots?q=&bucket=&category=&ending=soon&affiliate=19,7`, `GET /lots/{id}`, `POST /lots/{id}/reprice`, `POST /lots/{id}/enrich`, `GET /feed?filter=` |
| API | `POST/GET /search`, `POST/GET /intent`, `GET /sites/auctions`, `POST /sites/refresh` |
| Events | `bidrl.auction.collected`, `bidrl.lot.analyzed`, `bidrl.lot.priced`, `bidrl.lot.enriched`, `bidrl.deal_found`, `bidrl.scan.completed`, `bidrl.bids.refreshed`, `bidrl.search.completed`, `bidrl.intent.completed`, `bidrl.sites.discovered`, `bidrl.expired.cleaned` |
| UI | `/bidrl` feed, `/bidrl/auctions`, `/bidrl/lots`, `/bidrl/findings`, `/bidrl/watchlists`, `/bidrl/saved`, `/bidrl/auction/:id`, `/bidrl/lot/:id` |

Allowlisted hosts: `www.bidrl.com`, `bidrl.com`, `d3ugkdpeq35ojy.cloudfront.net`. The fake
browser serves a canned three-lot warehouse auction at
`https://www.bidrl.com/auction/42/bidgallery`, plus `POST /api/ItemData` and
`GET /aucbeat/pusher/` fixtures. Before scanning, connect a provider and
assign models to the plugin's five declared routes, `cheap-vision` (chat+vision),
`grounded-price` (chat), `intent-expand` (chat), `intent-match` (embed), and
`watch-judge` (chat). The plugin screen lists each by purpose; Models will too. Do not
invent other names — the plugin asks for these five. Assign `intent-expand` to a cheap chat model and `intent-match` to
an embedding model (for example `text-embedding-3-small`).

---

## Search

Search is user-triggered (`POST/GET /search`). It does not crawl BidRL on a schedule
and is not on the feed UI.

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

## Watchlists and findings

A watchlist is a saved intent plus the rules that keep its queue short: locations,
categories, a price ceiling, and a score floor. Running one is still user-triggered
(`POST /watchlists/{id}/run`) — there is no cron yet.

A run is a **funnel, cheapest stage first**, so its cost tracks what you asked for rather
than how much BidRL listed:

| Stage | Cost | Drops |
|---|---|---|
| Rules | free, SQL | Lots outside the watchlist's locations, categories, or price ceiling — and any lot already decided for it |
| Embedding rank | one embed per changed lot | Everything below the watchlist's `minScore`, then everything past the judge cap of 40 |
| `watch-judge` | one cheap chat call per survivor | Listings that merely share a word with what you described |
| Vision, then pricing | the existing scan path | — runs only on what came through all three |

That ordering is the whole cost argument: a 400-lot warehouse auction costs one embed per
lot and a handful of chat calls, not 400 vision calls.

The judge is a **filter, not a scorer**, and it sees text only — never photographs. Asking
a cheap chat model to rank things it cannot see produces confident noise, the same failure
this plugin already refuses for pricing. Its sentence is stored and shown as the model's
words, attributed. A judge that errors does not silently drop its candidate: the ranking
already liked it, so it stays with the ranking's own reason.

The expansion is cached on the watchlist and refreshed only when the query text changes or
the cache passes 30 days, so a repeated run does not pay `intent-expand` again to be told
"tent, lantern, cooler".

`UNIQUE (watchlist_id, lot_id)` is what makes the queue usable: a lot decided once never
returns to it, and the rules stage excludes decided lots before they cost anything.
Accepting a finding also saves the lot — accepting something and then hunting for it in
another tab is the obvious wrong flow. Accept and reject are direct writes, not jobs.

An undecided finding pins its lot against "Remove ended", the same way a save does: a
finding is the record of what a watchlist turned up, and deleting the lot before you have
looked would empty the queue of exactly what it exists to show you.

## Automation

Two scheduled jobs, and a long list of reasons either might do nothing.

| Job | Cron (UTC) | Touches BidRL | Does |
|---|---|---|---|
| `sweep` | `0 */6 * * *` | yes | Lists open auctions at the chosen locations, then collects ones not already stored, soonest to close first |
| `match` | `30 */6 * * *` | no | Runs every enabled watchlist's funnel over what is collected |

They are separate and offset on purpose. `sweep` is the only scheduled work that reaches
the origin and must stop when BidRL says so; `match` never reaches it and should still run
while a sweep is latched, because there is usually a backlog of collected lots no
watchlist has looked at.

**`sweep` does nothing unless every guard passes.** Automation off (the default), no
locations chosen, or a throttle latch each end the tick before a browser session is even
opened. An empty location list is not treated as "everywhere": an unscoped scheduled
discovery is a crawl, which is the thing this plugin will not do. `maxAuctionsPerSweep`
(5) and `maxNewLotsPerSweep` (400) bound one tick; whatever does not fit waits for the
next one.

**The pacing is not relaxed for being scheduled.** One request in flight, 400ms apart,
three consecutive 429/403 responses stop the job. What is new is the **throttle latch**:
when that stop happens, the plugin writes `throttled_until` 24 hours out, and later ticks
return immediately without opening a session. No tick clears its own stop — only "Resume
now" on the Overview does, because the whole value of the latch is that it holds until a
person has looked. The pacer's stop is a wrapped sentinel error rather than a string, so
the sweep can tell "BidRL is refusing" apart from any other failure; only the first
latches.

A scoped discovery replaces the cache **only for the locations it visited**. Clearing the
whole table would delete listings for locations that run never looked at, which then read
as closed.

One auction failing does not abandon the tick, and one watchlist failing does not stop the
others — a budget refusal or an unassigned model on one watchlist is recorded on its own
row. `GET /automation` reports the schedule, both last ticks with what they did, the
latch, and the count waiting in Findings; the Overview shows that as a strip.

The cadence is fixed rather than configurable: job schedules are declared at registration,
before plugin config is readable. `automation.enabled` is the switch that matters.

## Intent

Intent matching is user-triggered (`POST/GET /intent`) from the lots catalog. It does not
hit BidRL and does not look at photographs.

Ask starts with one cheap `intent-expand` chat call: "camping" becomes related auction-title
words (tent, headlamp, lantern, canopy, cooler). The typed query and each related word are
then embedded as separate probes (at most twelve), and every collected lot is scored against
its closest probe using vectors stored in SQLite (`bidrl_lot_embeddings`). Related words
count slightly less than the words the user typed. The probes are kept apart on purpose:
embedding the query and its expansion as one string averages every related product into a
vector that sits about the same distance from the whole catalog, which reads as random
results. Each lot is embedded from its title, identification, model, category, and search
terms, plus a clipped description — a long boilerplate description otherwise buries the
title. The vector is reused until that text changes. Photographs are never sent.

Keyword overlap is folded on top of the vector score, so a lot whose title actually says
the word outranks a merely thematic neighbour, and the tail is cut relative to the best
match rather than at a fixed cosine — a fixed cut admits most of the catalog in an order
that looks arbitrary.

A scan is not required. A title that says "camping tent" matches, and so does a headlamp
that never uses the word camp, because expansion named it before embedding. Stored
photograph identifications count when they already exist, but they do not win over a
useful title.

The "Why" column names the bridge rather than echoing the lot: which probe pulled the lot
in, and what in the lot connects to it — `"lantern" in the title, related to camping`,
`photos show Herman Miller Aeron`, `listed as "aeron"`, `category Furniture`, or
`reads like sleeping bag, related to camping` when nothing textual overlaps. A bare list
of shared words, or "similar to" the lot's own title, says nothing the row does not
already show.

The host does not load a SQLite vector extension (virtual tables are denied). Vectors are
BLOBs; ranking is a local dot product over normalized float32 rows. Ended lots are skipped
at Ask and their stored vectors are deleted then; "Remove ended" also deletes the lot row
and its vector together. Lots with no end time are left alone.

If expansion fails, Ask embeds the typed words. If embedding fails, it matches the expanded
words against titles. The catalog's Find box stays a direct text filter.

Intent has its own tab (`/bidrl/intent`) with a few seeded intents beside the box, since the
useful thing to type is a purpose and not a keyword. Ask sends on Enter.

Navigation inside the plugin routes rather than reloading: the screens use `Link` and
`useRouteParams` from `@cc/ui`, so clicking a lot keeps the event stream and the snapshot
cache alive instead of reloading the whole application.

---

## Overview and catalog

`/bidrl` is an overview, not a fourth listing of the same table: counts, the widest gaps,
what closes next, and a link into a filtered catalog for each. With nothing collected it
says so and points at Auctions. Every figure on it is a link to the catalog URL that
proves it.

It is one request. `GET /overview` counts in SQL and returns only the two short lists,
because computing a front page in the browser means shipping every lot row — descriptions,
valuations, photo URLs — to count them, which is the wrong thing to put on a phone.

`/bidrl/lots` is the one catalog. It carries both the named presets and the field filters,
because they were always the same query — `GET /feed?filter=deals` and
`GET /lots?bucket=priced` ran the same SQL. `feedWhere` is now shared by both handlers and
`/lots` accepts `filter=` too. Live BidRL keyword search stays API-only
(`POST/GET /search`); no screen queues it.

| Preset | Means |
|---|---|
| All lots | everything collected, scanned or not |
| Best deals | priced, large gap between bid and the comparable (eBay first) |
| Worth opening | visually interesting, deliberately unpriced |
| Model number found | `exact_text` basis, highest confidence tier |
| Likely mislabeled | high disagreement between title and photos |
| All scanned | every lot a scan has looked at |

`filter=all` means every lot, including `pending`. The old feed value for "scanned but not
necessarily priced" is now spelled `filter=scanned`.

**Location is stored, not joined.** `bidrl_auctions` carries `affiliate_id`,
`affiliate_name`, and `city`, stamped at collect time from the SITES discovery cache and
backfilled by every later discover. It used to be a read-time join against
`bidrl_affiliate_auctions`, which discover wipes and rebuilds on every run — so a collected
auction lost its location the moment it closed or dropped off its landing page. Lots read
the location from their own auction.

`affiliate=` takes several ids at once, comma-joined or repeated, because the useful
question is what is within a drive: a handful of locations, not one and not all of them. No
parameter means every location. The catalog's location chips are seeded from `GET /locations`
rather than from the lot list, so selecting one does not delete the rest from the filter.
Every card and row shows its location, and it stays below 720px — ruling a lot out by the
drive rather than the price is exactly what you do from a phone. The lot page shows it as a
link into the catalog filtered to that location. Distance is not modelled: the plugin does
not know where you live and does not geocode to guess.

**Saving a lot outlives its auction.** A star on every card, row, and lot page writes to
`bidrl_favorites` directly rather than queueing a job — it is instant and local, and the
rule that every button queues a job is there for work that takes time. `/bidrl/saved` is
that list, newest save first, with the same location and category filters as the catalog
and no similar-lot collapsing, because every row on it was chosen on purpose. A note per
lot lives on the lot page; writing one saves the lot, since a note about something you did
not keep is not a thing anyone means to write.

**"Remove ended" never deletes a saved lot.** The point of saving something is to refer
back to it later, and later is usually after it closed — a favourite that vanishes on the
next tidy is worse than no favourite. An ended auction holding a saved lot is kept as its
shell so the lot keeps its photos, comparable, and location; that auction's unsaved lots
still go, and cleanup reports what it kept as well as what it removed. Deleting an auction
outright still takes everything in it, saved lots included: that was asked for.

Find filters the visible lots by title, identification, model, and category without
starting a BidRL search. The catalog's whole state — preset, text, bucket, category,
ending — lives in the query string, so a filtered list can be linked to and pasted, and
opening a lot and coming back returns the list rather than resetting it.

Every button on these screens queues a job rather than doing the work, so every button says
what it queued and links to the job — they used to post and say nothing, which reads as a
dead button. `GET /auctions/{id}/index` lists an auction's lot ids in screen order and
nothing else, so the lot page can offer previous/next and "3 of 40" without downloading
every full lot row of a large auction.

Lots on the feed, catalog, and auction page switch between a card grid and a table. The
choice is remembered. Table columns sort on click: lot code, name, bid, expiration, price,
and the rest. Duplicate or near-duplicate listings — same model, identification,
or long identical title — collapse to one representative with the extras behind "N similar".

Every card or row shows a thumb, the BidRL title beside what the photos suggest, the
current bid, a local countdown from stored `ends_at`, the auction's location, category, and a link back to BidRL.
On a card the gap rides the photograph as a pill — green past 50%, amber past 20%, quiet
below that, because a thin gap does not survive a buyer's premium — and the bid is the only
large figure, with the comparable beside it as "vs $X sold · eBay". A lot whose `ends_at`
has passed carries an "Ended" pill opposite the gap.

Below 720px the lot table drops lot code, category, comparable, and bucket — but keeps the
star and location — rather than scrolling sideways past the bid — those live on the lot page — the card grid tightens to
150px columns, and each filter takes its own row.

`/bidrl/auctions` shows collected auctions first, grouped by SITES location, then
paste-a-URL collect, then open SITES auctions grouped the same way. "Remove ended"
deletes closed auctions, leftover closed lots, and stale SITES rows. `/bidrl/lots` is the
catalog: intent matching over embedded titles (and identifications when already scanned), then
local text, bucket, category, and ending-soon (open lots ending within 24 hours).
Auction and lot views add a bidder card (high bidder, bid count, min bid, reserve,
extended) and Open on BidRL. The lot page shows photos in a large stage with a thumbnail
strip; arrow keys and Prev/Next move between them, and clicking the photo opens a
full-window view.

---

## Acceptance criteria

### Pricing evidence

- A numeric valuation is stored only for `exact_text` or `barcode` identification, and only
  from a search hit that names the model and a dollar amount. Hits are ranked
  eBay, then retail, then other resale, then the open web. The model's
  `price_cents` must match a `$` amount in that hit.
- Missing or mismatched evidence leaves the lot unpriced. The feed shows the source site,
  quote, retrieval age, and whether the listing is asking or sold. It never presents an
  uncited model estimate as a market price.
- Repricing creates a new evidence record rather than overwriting the prior one, so a
  displayed valuation can be audited against the evidence used at that time. Reprice
  always searches; a scan reuses a comparable for the same model looked up in the last
  seven days unless the photos or title say the lot is impaired (for parts, broken).

### Resource limits

- Collection rejects more than 500 lots per auction, more than 12 images per lot, an image
  over 10 MiB, or more than 100 MiB of images for one lot; rejected items are recorded with
  a visible reason.
- With `automation.enabled` false, or no locations chosen, or the latch set, a `sweep`
  tick returns without opening a browser session; three consecutive 429/403 responses on a
  tick set the latch, and only `POST /automation/resume` clears it.
- Scan, reprice, bid-refresh, enrich, search, intent, and SITES-discover job definitions are enqueue-only, have concurrency `1`
  per operation, and time out after two hours. The scan performs at most four concurrent
  AI calls and stops admitting calls when its context is cancelled or budget reservation
  fails. Intent matching makes one `intent-expand` chat call to name related gear, embeds
  the query and each related word as its own probe, and embeds each collected lot whose
  title (or stored identification) has changed, then ranks locally on the closest probe
  plus keyword overlap and keeps only what stays close to the best match. It does not re-read
  photographs. Vectors live in `bidrl_lot_embeddings`, are skipped and dropped when a lot
  has ended, and are deleted with the lot on "Remove ended".
- ItemData and pusher traffic to BidRL is paced at 400ms with one request in flight. Three
  consecutive HTTP 429 or 403 responses stop the job.
- Disabling BIDRL makes new plugin HTTP requests return `503`, blocks new jobs, AI
  dispatches, and browser sessions, closes admitted browser sessions, and cancels running
  job contexts. A paid call admitted before disable may finish; its usage is stored and
  its budget reservation is settled.
