# Plugin: `bidrl` — automation, findings, favorites, locations

Status: steps 1–2 (locations, favorites) implemented; steps 3–4 proposed · 2026-09-01 · extends [`bidrl.md`](bidrl.md)

Four changes to the BIDRL plugin, in dependency order:

1. **Locations become first-class** — denormalized onto auctions and lots, filterable to a
   set of locations rather than all-or-one.
2. **Watchlists** — a saved intent plus structured rules ("camping gear, under $80, Turlock
   or Modesto").
3. **Automation** — cron collects from chosen locations; AI runs only on watchlist hits.
4. **Findings and Saved** — a review queue you accept or reject, and favorites you keep.

---

## 0. The decision this reverses

`bidrl.md` states that every v1 action is user-triggered, that the manifest sets
`Automated: false`, and that there is "no cron and no BidRL poll" — reasoning that BIDRL's
user agreement prohibits automated processes that monitor or copy its pages.

This proposal introduces cron. That is a deliberate reversal by the operator for a
single-user, self-hosted instance, and it is not free: the manifest flips to
`Automated: true`, which under [`plugin-api.md`](../plugin-api.md) forces the plugin to
carry a budget, and the plugin becomes something that touches a third party while nobody is
watching. The mitigations are not decoration — they are the reason this is defensible:

- **Cron collects only from locations you explicitly selected.** Not the SITES menu, not
  everything. An empty selection means the cron does nothing at all.
- **The existing pacing is not relaxed.** One request in flight, 400ms minimum spacing,
  three consecutive 429/403 responses stop the job. Automation makes that rule *more*
  important, not less; a scheduled job that trips it must also refuse to reschedule until
  the operator clears it (see §3, throttle latch).
- **Nothing is re-collected while it is unchanged.** Cron collects lots it has never seen
  from auctions it already knows, and enumerates new auctions at your locations. It does not
  re-walk collected auctions to refresh bids; the local countdown from stored `ends_at`
  still does that job.
- **A visible global switch.** `automation.enabled` in plugin config, default `false`.
  Disabling the plugin already cancels running jobs and closes browser sessions; the config
  flag makes stopping the schedule a smaller action than disabling the whole plugin.

`bidrl.md`'s "Every v1 action is user-triggered" section gets rewritten rather than left
to contradict this file.

---

## 1. Locations, properly

### The bug this fixes first

Location today is a `LEFT JOIN bidrl_affiliate_auctions s ON s.id = a.id` in `http.go`, and
`replaceAffiliateAuctions` does `DELETE FROM bidrl_affiliate_auctions` on every discover.
So a collected auction shows its location only while it is still open and still listed on
its affiliate landing page. Once it closes — or once a discover runs while BidRL is having
a bad day — the location silently becomes blank. Lots never had a location at all.

Location must be **stored on the record at collect time**, not joined at read time.

### Schema (migration 7)

```sql
ALTER TABLE bidrl_auctions ADD COLUMN affiliate_id   TEXT NOT NULL DEFAULT '';
ALTER TABLE bidrl_auctions ADD COLUMN affiliate_name TEXT NOT NULL DEFAULT '';
ALTER TABLE bidrl_auctions ADD COLUMN city           TEXT NOT NULL DEFAULT '';
CREATE INDEX bidrl_auctions_affiliate ON bidrl_auctions(affiliate_id);
```

Lots get location by joining their own auction — a stable local join, unlike the current
one. `GET /lots` and `GET /feed` add `affiliateId`, `affiliateName`, and `city` to each
row; `feedWhere` (already shared by both handlers) grows an `affiliate` clause.

The migration backfills from whatever `bidrl_affiliate_auctions` still holds; anything
already lost stays blank until a discover fills it in. `stampAuctionLocation` copies the
cache row onto the auction inside the collect transaction, and `replaceAffiliateAuctions`
calls it again for every listed auction, so a discover backfills auctions collected from a
pasted URL — which were never on a landing page when they were collected. A cache miss
leaves the stored value alone rather than blanking it. ItemData carries no affiliate field,
so there is no per-lot fallback and none is worth an extra request.

### API

`GET /lots?affiliate=19,7` — repeatable or comma-joined, **multiple locations**, matching
the existing multi-value filter style. Same parameter on `/feed` and `/findings`. Absent
means every location, which keeps existing links working.

### UI

- Every card and every table row shows the location, beside the countdown. In the table it
  is its own sortable column, and unknown locations sort last in either direction. Under
  720px the location **stays** — the whole point is ruling a lot out before opening it,
  which is exactly what you do on a phone. Lot code, category, comparable, and bucket drop
  instead; they are on the lot page.
- The catalog gains location toggle chips beside bucket and category — several at once is
  the normal selection, so they are toggles rather than a select. They are seeded from
  `GET /locations`, its own request: deriving the list from the filtered lots would delete
  every unselected location from the filter the moment you picked one. State lives in the
  query string like every other filter, so a two-location list is a linkable URL.
- The lot page shows the location as a link into the catalog filtered to it.
- Distance is deliberately **not** modelled. The plugin does not know where you live and
  should not start geocoding to guess; you know which names are a 30-minute drive. If that
  changes, a `homeCity` config and a static per-affiliate drive-time table is the additive
  step — not a maps API.

---

## 2. Watchlists

A watchlist is the saved form of what `/bidrl/intent` does once.

```sql
CREATE TABLE bidrl_watchlists (
  id              TEXT PRIMARY KEY,
  name            TEXT NOT NULL,
  query           TEXT NOT NULL,               -- "camping gear", "an electric scooter"
  enabled         INTEGER NOT NULL DEFAULT 1,
  affiliate_ids   TEXT NOT NULL DEFAULT '[]',  -- JSON array; empty means every location
  categories      TEXT NOT NULL DEFAULT '[]',  -- JSON array of vision categories
  max_bid_cents   INTEGER,                     -- null means no ceiling
  min_score       REAL NOT NULL DEFAULT 0.55,
  expansion       TEXT NOT NULL DEFAULT '[]',  -- cached intent-expand words
  expanded_at     TEXT NOT NULL DEFAULT '',
  last_run_at     TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL
) STRICT;
```

**Matching is a three-stage funnel**, cheapest first, so cost tracks your interests rather
than BidRL's volume:

| Stage | Cost | What it does |
|---|---|---|
| Rules | free, SQL | Drop lots outside the watchlist's locations, categories, and price ceiling, and any already ended. |
| Embedding rank | one embed per *new* lot | Exactly today's intent path: probes from the query plus cached expansion, cosine against `bidrl_lot_embeddings`, keyword overlap folded on top. Survivors above `min_score`. |
| Relevance check | one cheap chat call per candidate | A new `watch-judge` model route. Given the watchlist description and the lot's title, description, category, and identification, it answers `relevant` / `not relevant` with one sentence of reason. |

The expansion is cached on the watchlist and refreshed only when the query text changes or
the cache is older than 30 days — otherwise a nightly cron pays for `intent-expand` every
night to get the same five words back.

The judge is a **filter, not a scorer**. It sees text only, never photographs; asking a
cheap chat model to rank things it cannot see produces confident noise, which is the same
failure `bidrl.md` already refuses for pricing. Its stored reason is shown on the finding as
the model's words, attributed — not as a fact about the lot.

Vision analysis and grounded pricing then run **only on lots that survive all three stages**
and are not already analyzed, through the existing `analyzeUnseen` / `priceEligible` path.
That is the whole cost argument: a 400-lot warehouse auction costs one embed per lot and a
handful of chat calls, not 400 vision calls.

---

## 3. Automation

### Jobs

Two new cron job definitions, added to `Jobs()`:

| Job | Schedule | Does |
|---|---|---|
| `sweep` | `0 */6 * * *` (configurable) | Discover open auctions at the selected locations; collect lots not yet stored, oldest-ending auction first. |
| `match` | `30 */6 * * *` | For each enabled watchlist, run the funnel over lots seen since its `last_run_at`; scan and price survivors; insert findings. |

They are separate on purpose. `sweep` touches BIDRL and must stop on 429/403; `match` never
touches BIDRL at all and should still run if `sweep` was throttled.

`Schedule` and `TimeZone` are set on the `hostjobs.Def` — the host's cron already rechecks
policy in its insertion transaction and drops ticks while the plugin is disabled, so the
kill switch needs no plugin-side help. Concurrency stays `1`.

### Budgets and limits

- Manifest flips to `Automated: true` and the plugin gets a budget. A `match` run that
  fails budget reservation stops admitting AI calls and records why on the run, rather than
  half-finishing quietly.
- `automation.maxAuctionsPerSweep` (default 5) and `automation.maxNewLotsPerSweep`
  (default 400) bound one tick. A sweep that hits a cap logs it and leaves the rest for the
  next tick.
- **Throttle latch.** Three consecutive 429/403 responses already stop the job. When that
  happens on a cron tick, the plugin writes a `throttled_until` timestamp (24h) into
  `bidrl_automation_state`, and subsequent ticks return immediately without opening a
  browser session. The Automation strip shows the latch and offers "resume now". A
  scheduled job that quietly retries into a ban is worse than one that stops and says so.

### Events

`bidrl.sweep.completed`, `bidrl.match.completed`, `bidrl.finding.created`,
`bidrl.finding.decided`. `bidrl.finding.created` is what a notification rule would hang off
later; this proposal does not add notifications.

---

## 4. Findings and Saved

```sql
CREATE TABLE bidrl_findings (
  id            TEXT PRIMARY KEY,
  watchlist_id  TEXT NOT NULL,
  lot_id        TEXT NOT NULL,
  lot_title     TEXT NOT NULL DEFAULT '',    -- snapshot; survives "Remove ended"
  lot_thumb_url TEXT NOT NULL DEFAULT '',
  score         REAL NOT NULL,
  reason        TEXT NOT NULL DEFAULT '',    -- the judge's sentence, as the model's words
  state         TEXT NOT NULL DEFAULT 'new'
                CHECK (state IN ('new','accepted','rejected')),
  decided_at    TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL,
  UNIQUE (watchlist_id, lot_id)
) STRICT;
CREATE INDEX bidrl_findings_state ON bidrl_findings(state, created_at);

CREATE TABLE bidrl_favorites (
  lot_id      TEXT PRIMARY KEY,
  note        TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL
) STRICT;
```

The `UNIQUE (watchlist_id, lot_id)` is the thing that makes the queue usable: a lot rejected
once never returns to it, no matter how many times cron re-matches. Rejections are also fed
back — a lot rejected for a watchlist is skipped at the rules stage on later runs, before it
costs an embed or a judge call.

**Findings survive their lot.** A finding whose lot has ended stays in the queue marked
`Ended` until you decide on it; that is the record of what the automation found while you
were asleep.

Favorites solve this by **keeping the lot rather than snapshotting it** — the built
behaviour, and better than the title-and-thumb snapshot this file first proposed. Cleanup
skips a saved lot and keeps its auction as a shell, so the saved row keeps its photos,
comparable, and location instead of degrading to a title and a dead thumbnail. Findings
will do the same: a finding pins its lot against cleanup for as long as it is undecided.
Deleting an auction outright still takes everything in it, saved lots included — that was
asked for explicitly, unlike a tidy.

### API

```
GET    /watchlists                 POST  /watchlists
GET    /watchlists/{id}            PATCH /watchlists/{id}      DELETE /watchlists/{id}
POST   /watchlists/{id}/run        run the funnel now, without waiting for cron
GET    /findings?state=&watchlist=&affiliate=&sort=
POST   /findings/{id}/accept       POST  /findings/{id}/reject
POST   /lots/{id}/favorite         DELETE /lots/{id}/favorite
GET    /favorites?sort=&affiliate=&category=
GET    /automation                 status, next tick, throttle latch
POST   /automation/resume          clear the latch
```

Accept, reject, and favorite are direct writes, not jobs — they are instant and local, and
the "every button queues a job" rule in `bidrl.md` exists for work that takes time.

### UI

Tabs become **Overview · Findings · Auctions · Lots · Saved · Intent**, plus
`/bidrl/watchlists` reached from Findings.

- **Findings** is the review queue: newest first, grouped by watchlist, each row the
  existing lot card plus the judge's reason and Accept / Reject. Accepting favorites the lot
  and clears it from the queue — accepting and then hunting for it in another tab is the
  obvious wrong flow. Keyboard `a` / `r` / `j` / `k`, because a queue is something you work
  through. Filters: watchlist, location, state.
- **Saved** is favorites, reusing the catalog's card/table toggle, sorting, and URL state —
  sort by ending soonest, bid, gap, location, or when you saved it. A note field per
  favorite.
- The star sits on every lot card, row, and lot page, so favoriting works from anywhere and
  not only from a finding.
- `/bidrl` overview gains an automation strip: last sweep, next tick, new-findings count
  (linking into Findings), and the throttle latch if it is set.

---

## Build order

Each step is independently useful and independently shippable.

1. **Locations** (§1) — *done.* Migration 7, collect-time capture, `affiliate` filter, UI
   column and location chips. Fixes an existing bug and delivers the "rule it out without
   opening it" ask on its own.
2. **Favorites** (§4, partial) — *done.* Table, star, Saved tab, per-lot note. No AI, no
   cron. Cleanup keeps saved lots instead of snapshotting them (see §4).
3. **Watchlists + manual run** (§2) — the funnel, `watch-judge` route, findings table,
   Findings tab, `POST /watchlists/{id}/run`. Everything works, still user-triggered.
4. **Cron** (§3) — `sweep` and `match` schedules, `Automated: true`, budget, throttle latch,
   automation strip. Only now does the plugin touch BIDRL unattended, and by then every part
   of what it will do has been watched doing it by hand.

Step 4 last is the point. If the funnel produces junk, you find out in step 3 while you are
still clicking the button.

---

## Acceptance criteria

- A collected auction shows its location after it closes and after a discover has wiped the
  SITES cache. A lot shows its auction's location on the card, the table row, the lot page,
  and below 720px.
- `GET /lots?affiliate=19,7` returns lots from exactly those two locations; the catalog's
  location multi-select round-trips through the query string.
- A watchlist run over a collected auction makes at most one `intent-expand` call (none if
  the cached expansion is fresh), one embed per lot whose text changed, one `watch-judge`
  call per rule-and-embedding survivor, and vision or pricing calls only for lots the judge
  passed.
- A rejected finding does not reappear after a later `match` run over the same lot.
- Three consecutive 429/403 responses during a cron `sweep` set the throttle latch, later
  ticks return without opening a browser session, and the UI says so with a resume control.
- With `automation.enabled` false, or the plugin disabled, no cron tick performs work; a
  tick dropped for policy is not retried as a backlog.
- Favorites survive "Remove ended" and survive deletion of the finding that created them.
