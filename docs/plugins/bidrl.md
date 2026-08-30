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
  price/           grounded search for current market price
  score/           deal score + mislabel score
  store/           migrations, queries
  ui/              feed, auction view, lot detail
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
                          price        grounded search, separate call
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

**Collection is user-initiated per auction**, with permanent caching of images and
analysis. Bid prices refresh cheaply; identification never re-runs. This is both the
sensible engineering choice and the respectful one — BIDRL's user agreement prohibits
automated processes that monitor or copy its pages, so a "scan this auction" action with
aggressive caching is the right posture, not a continuous crawler.

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

**Vision and pricing are separate calls.** The vision model says what it sees; a grounded
call finds the current price. One call doing both produces invented MSRPs.

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
| `AI()` grounded | the price stage |
| `Jobs()` cron + long-running with progress | scans and bid refreshes |
| `Events()` | `bidrl.deal_found`, `bidrl.lot.analyzed`, `bidrl.alert` |
| `Store()` / `Blobs()` | lots, analyses, cached photos |
| Budgets + kill switch | `Automated: true`; a runaway scan is exactly what they guard against |
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
