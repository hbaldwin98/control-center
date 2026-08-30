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
  collect/         Playwright: auction enumeration, lot pages, images
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

**Playwright lives inside this plugin, not in core.** One consumer is not enough
information to design a shared browser API. When a second plugin wants a browser, it
becomes a capability.

**Every v1 action is user-triggered.** Collection starts only from "add auction", a scan
starts only from "scan", pricing starts only inside that requested scan or from "reprice",
and bids refresh only from "refresh bids". There is no cron or event-triggered work, and
the backend manifest sets `Automated: false`. Jobs still make each requested operation
durable, cancellable, budgeted, and subject to the host-capability kill switch.

Images and analyses are cached until the user deletes the auction; identification does
not rerun during a bid refresh. Deletion removes the auction's records and blobs. The
storage module's finite per-plugin quota applies, and collection stops visibly rather than
evicting audit evidence when the quota is full. This is both the sensible engineering
choice and the respectful one: BIDRL's user agreement prohibits automated processes that
monitor or copy its pages, so explicit user actions with aggressive caching replace
continuous crawling.

**Browser targets use a fixed HTTPS host allowlist.** The plugin accepts BIDRL auction and
lot URLs only when their parsed host is in the compiled allowlist and their scheme is
`https`. Browser request interception applies the same rule to redirects and subresources,
with only the fixed BIDRL/CDN hosts required by collection. Userinfo, alternate ports, IP
literals, and all other origins are rejected. This prevents supplied URLs from turning
Playwright into a browser SSRF primitive.

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
| `AI()` vision, multi-image, structured output | the analyze stage |
| `AI()` grounding options and citations | the price stage |
| `Jobs()` enqueue-only, long-running with progress | user-triggered collection, scans, pricing, and bid refreshes |
| `Events()` | `bidrl.deal_found`, `bidrl.lot.analyzed`, `bidrl.alert` |
| `Store()` / `Blobs()` | lots, analyses, cached photos |
| Budgets + host-capability kill switch | durable user-triggered work is still admitted, reserved, and cancellable |
| UI | a feed with filters, an auction view, a lot detail |

If this can be built without punching a hole through the `Host` facade, the boundary is
right.

---

## Feed

The default view is not a search box. It is a treasure-hunting feed:

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
- Scan, reprice, and bid-refresh job definitions are enqueue-only, have concurrency `1`
  per operation, and time out after two hours. The scan performs at most four concurrent
  AI calls and stops admitting calls when its context is cancelled or budget reservation
  fails.
- Disabling BIDRL makes new plugin HTTP requests return `503`, blocks new jobs and AI
  dispatches, and cancels running job contexts. A paid call admitted before disable may
  finish; its usage is stored and its budget reservation is settled.
