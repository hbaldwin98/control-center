import { parseInstant } from "@cc/ui";

export type Auction = {
  id: string;
  url: string;
  title: string;
  status: string;
  lotCount: number;
  lastError: string;
  collectedAt: string;
  endsAt: string;
  affiliateId: string;
  affiliateName: string;
  city: string;
};

export type Lot = {
  id: string;
  auctionId: string;
  url: string;
  lotCode: string;
  title: string;
  description: string;
  currentBidCents: number | null;
  minBidCents: number | null;
  bidIncrementCents: number | null;
  bidCount: number;
  highBidder: string;
  endsAt: string;
  biddingExtended: boolean;
  reserveMet: boolean;
  category: string;
  bucket: string;
  affiliateId: string;
  affiliateName: string;
  city: string;
  identification: string;
  basis: string;
  modelOrSku: string;
  titleAgreement: number;
  mislabelScore: number;
  priceCents: number | null;
  priceKind: string;
  sourceUrl: string;
  citedText: string;
  sourceTitle: string;
  sourceClass: string;
  sourceLabel: string;
  reusedFromLotId: string;
  retrievedAt: string;
  dealScore: number | null;
  matchScore?: number | null;
  matchReason?: string;
  thumbUrl: string;
  favorite: boolean;
  favoriteNote: string;
  savedAt: string;
  photoUrls?: string[];
  latestEventId?: number;
};

export type LotsPage = {
  lots: Lot[];
  latestEventId: number;
};

export type FavoritesPage = {
  lots: Lot[];
  latestEventId: number;
};

export type Automation = {
  enabled: boolean;
  locations: number;
  affiliateIds: string[];
  maxAuctionsPerSweep: number;
  maxNewLotsPerSweep: number;
  sweepSchedule: string;
  matchSchedule: string;
  timeZone: string;
  lastSweepAt: string;
  lastSweepNote: string;
  lastMatchAt: string;
  lastMatchNote: string;
  throttledUntil: string;
  throttled: boolean;
  newFindings: number;
  queuedAuctions: number;
  watchlists: number;
  enabledWatchlists: number;
};

export type AutomationPage = {
  automation: Automation;
  latestEventId: number;
};

/**
 * What the automation strip says in one line. The three "it is doing nothing" cases
 * are spelled out separately: an operator who turned automation on and chose no
 * locations must not read the same sentence as one who turned it off.
 */
export function automationSummary(a: Automation): string {
  if (a.throttled) return "Stopped: BidRL is refusing requests.";
  if (!a.enabled) return "Off. Nothing runs on a schedule.";
  if (a.locations === 0) return "On, but no locations chosen — the sweep does nothing.";
  const where = `${a.locations} location${a.locations === 1 ? "" : "s"}`;
  return `On, sweeping ${where} every six hours.`;
}

/**
 * What the next sweep would do, said as a plan rather than as settings. Every way it
 * can come to nothing — off, unscoped, latched, nothing new at the chosen places — is
 * its own sentence, because "nothing will happen" is the answer an operator most often
 * needs and the four reasons want different fixes.
 */
export function sweepPlan(a: Automation): string {
  if (a.throttled) return "Held until you resume it. No tick will touch BidRL.";
  if (!a.enabled) return "Automation is off, so this tick returns without doing anything.";
  if (a.locations === 0) return "No locations chosen. An unscoped scheduled crawl is not something this plugin does.";
  if (a.queuedAuctions === 0) return "Nothing new at your locations. The next tick will find nothing to collect.";
  const auctions = Math.min(a.queuedAuctions, a.maxAuctionsPerSweep);
  return `${auctions} auction${auctions === 1 ? "" : "s"} next, soonest to close first, stopping at ${a.maxNewLotsPerSweep} new lots.`;
}

/** The same, for the match tick — which runs even while a sweep is latched. */
export function matchPlan(a: Automation): string {
  if (!a.enabled) return "Automation is off, so this tick returns without doing anything.";
  if (a.watchlists === 0) return "No watchlists yet. Write one and the match tick has something to run.";
  if (a.enabledWatchlists === 0) return `All ${a.watchlists} watchlists are paused, so nothing runs.`;
  const n = a.enabledWatchlists;
  return `${n} of ${a.watchlists} watchlist${a.watchlists === 1 ? "" : "s"} run against everything collected since they last looked.`;
}

/** Location names for the ids automation is scoped to, falling back to the raw id. */
export function automationLocations(a: Automation, labels: Map<string, string>): string[] {
  return a.affiliateIds.map((id) => labels.get(id) ?? id);
}

export type Watchlist = {
  id: string;
  name: string;
  query: string;
  enabled: boolean;
  affiliateIds: string[];
  categories: string[];
  maxBidCents: number | null;
  minScore: number;
  status: string;
  lastError: string;
  lastRunAt: string;
  createdAt: string;
  newFindings: number;
};

export type Finding = {
  id: string;
  watchlistId: string;
  watchlist: string;
  score: number;
  reason: string;
  state: string;
  createdAt: string;
  lot: Lot;
};

export type FindingsPage = {
  findings: Finding[];
  watchlists: Watchlist[];
  state: string;
  latestEventId: number;
};

export type WatchlistsPage = {
  watchlists: Watchlist[];
  latestEventId: number;
};

/**
 * Findings grouped by the watchlist that found them. A queue you work through reads
 * better in runs of one topic than interleaved: deciding on ten camping items in a row
 * is one judgement, alternating between camping and scooters is ten.
 */
export function groupFindings(findings: Finding[]): { id: string; label: string; findings: Finding[] }[] {
  const map = new Map<string, { id: string; label: string; findings: Finding[] }>();
  const order: string[] = [];
  for (const f of findings) {
    let group = map.get(f.watchlistId);
    if (!group) {
      group = { id: f.watchlistId, label: f.watchlist || "Watchlist", findings: [] };
      map.set(f.watchlistId, group);
      order.push(f.watchlistId);
    }
    group.findings.push(f);
  }
  return order.map((id) => map.get(id)!);
}

/** What a watchlist narrows to, in a line, so a card says its rules without a form. */
export function watchlistRules(
  w: Watchlist,
  locationLabels: Map<string, string>,
): string {
  const parts: string[] = [];
  if (w.affiliateIds.length > 0) {
    parts.push(w.affiliateIds.map((id) => locationLabels.get(id) ?? id).join(", "));
  }
  if (w.categories.length > 0) parts.push(w.categories.join(", "));
  if (w.maxBidCents != null) parts.push(`under ${cents(w.maxBidCents)}`);
  return parts.length > 0 ? parts.join(" · ") : "Anywhere, any category, any price";
}

export type FeedPage = {
  filter: string;
  q: string;
  lots: Lot[];
  latestEventId: number;
};

export const LOT_CATEGORIES = [
  "tools",
  "furniture",
  "electronics",
  "appliances",
  "outdoor",
  "automotive",
  "sporting",
  "household",
  "collectibles",
  "other",
] as const;

export type AuctionPage = {
  auction: Auction;
  lots: Lot[];
  latestEventId: number;
};

/** Just enough of an auction's lots to page through them from a lot screen. */
export type AuctionIndex = {
  title: string;
  lots: { id: string; lotCode: string; title: string }[];
};

export type AuctionsPage = {
  auctions: Auction[];
  latestEventId: number;
};

export type SearchHit = {
  lotId: string;
  auctionId: string;
  url: string;
  title: string;
  auctionTitle: string;
  lotCode: string;
  affiliateId: string;
  affiliateName: string;
  preferred: boolean;
  currentBidCents: number | null;
  matchScore: number;
  matchReason: string;
  source: string;
  collected: boolean;
};

export type SearchPage = {
  search: {
    id: string;
    query: string;
    scope: string;
    status: string;
    hitCount: number;
    lastError: string;
    createdAt: string;
  } | null;
  hits: SearchHit[];
  latestEventId: number;
};

export type IntentSearch = {
  id: string;
  query: string;
  status: string;
  scanned: number;
  skipped: number;
  hitCount: number;
  lastError: string;
  createdAt: string;
};

export type IntentPage = {
  search: IntentSearch | null;
  lots: Lot[];
  latestEventId: number;
};

export type SitesAuction = {
  id: string;
  url: string;
  title: string;
  affiliateId: string;
  affiliateName: string;
  city: string;
  itemCount: number;
  endsAt: string;
  collected: boolean;
};

export type SitesPage = {
  auctions: SitesAuction[];
  latestEventId: number;
};

export function eventBoundary(id: number | string | undefined): string {
  try {
    return BigInt(id ?? 0).toString();
  } catch {
    return "0";
  }
}

export function cents(n: number | null | undefined): string {
  if (n == null) return "—";
  return (n / 100).toLocaleString(undefined, { style: "currency", currency: "USD" });
}

/**
 * The named presets, in the order the catalog offers them. They are the same predicates
 * the feed endpoint has always had; the catalog now accepts them too, so a preset and the
 * bucket/category filters are one screen rather than two listings of the same table.
 */
export const LOT_PRESETS = [
  { value: "", label: "All lots", hint: "Every collected lot, scanned or not." },
  { value: "deals", label: "Best deals", hint: "Priced lots where the current bid is below a search listing that names the model and a price, preferring eBay sold comps." },
  { value: "worth_opening", label: "Worth opening", hint: "Visually interesting, deliberately unpriced." },
  { value: "model", label: "Model number found", hint: "A model number or barcode was read from a photo." },
  { value: "mislabeled", label: "Likely mislabeled", hint: "Title and photographs disagree." },
  { value: "scanned", label: "All scanned", hint: "Every lot a scan has looked at." },
] as const;

export function filterLabel(filter: string): string {
  return LOT_PRESETS.find((p) => p.value === filter)?.hint ?? LOT_PRESETS[0].hint;
}

/** What the overview endpoint counts, so the front page never ships the lot table. */
export type OverviewStats = {
  auctions: number;
  lots: number;
  scanned: number;
  unscanned: number;
  priced: number;
  live: number;
  ending: number;
};

export type OverviewPage = {
  stats: OverviewStats;
  deals: Lot[];
  closing: Lot[];
  latestEventId: number;
};

/** True when a close time falls inside the window ahead. No end time is never "soon". */
export function endsWithin(endsAt: string, now: number, windowMs: number): boolean {
  if (!endsAt) return false;
  const ms = parseInstant(endsAt);
  if (!Number.isFinite(ms)) return false;
  return ms > now && ms - now <= windowMs;
}

export function comparableHint(lot: Pick<Lot, "priceKind" | "sourceLabel" | "sourceUrl">): string {
  const kind = lot.priceKind === "sold" ? "sold" : lot.priceKind === "asking" ? "asking" : "";
  const where = lot.sourceLabel || sourceHost(lot.sourceUrl);
  if (kind && where) return `${kind} · ${where}`;
  return where || kind;
}

/**
 * How the deal gap reads at a glance. A gap is only interesting once it is wide enough to
 * survive a buyer's premium, so a thin one stays neutral rather than lighting up green.
 */
export function gapTone(score: number | null | undefined): "neutral" | "ok" | "warn" {
  if (score == null) return "neutral";
  if (score >= 0.5) return "ok";
  if (score >= 0.2) return "warn";
  return "neutral";
}

/** Percent, as the feed writes it: "42%". */
export function pct(score: number | null | undefined): string {
  if (score == null) return "";
  return `${Math.round(score * 100)}%`;
}

/** A lot whose close time has passed. No end time means still open, not ended. */
export function hasEnded(endsAt: string, now: number): boolean {
  if (!endsAt) return false;
  const ms = parseInstant(endsAt);
  if (!Number.isFinite(ms)) return false;
  return ms <= now;
}

/** Seeds for the Intent box. They describe a purpose, which is the whole point of it. */
export const INTENT_EXAMPLES = [
  "Things that would help me camp",
  "Set up a small woodworking shop",
  "Outfit a first apartment kitchen",
  "Gear for a road trip",
] as const;

export type Location = {
  id: string;
  affiliateName: string;
  city: string;
  lotCount: number;
};

export type LocationsPage = {
  locations: Location[];
  latestEventId: number;
};

export type LocationGroup<T> = {
  key: string;
  label: string;
  items: T[];
};

export function locationLabel(item: { affiliateName?: string; city?: string }): string {
  const name = (item.affiliateName ?? "").trim();
  const city = (item.city ?? "").trim();
  if (name && city && !name.toLowerCase().includes(city.toLowerCase())) {
    return `${name} · ${city}`;
  }
  return name || city || "Other locations";
}

/**
 * The location as shown on a lot, or "" when we do not know it. Distinct from
 * locationLabel, which falls back to "Other locations" for grouping — a lot with
 * no known location should say nothing rather than claim to be somewhere.
 */
export function locationLabelOrEmpty(item: { affiliateName?: string; city?: string }): string {
  const name = (item.affiliateName ?? "").trim();
  const city = (item.city ?? "").trim();
  if (!name && !city) return "";
  return locationLabel(item);
}

/** Comma-joined ?affiliate= value, parsed and re-serialized for the query string. */
export function parseAffiliateParam(raw: string | null | undefined): string[] {
  return [...new Set((raw ?? "").split(",").map((s) => s.trim()).filter(Boolean))];
}

export function affiliateParam(ids: string[]): string {
  return [...new Set(ids.filter(Boolean))].join(",");
}

export function groupByLocation<T extends { affiliateName?: string; city?: string }>(
  items: T[],
): LocationGroup<T>[] {
  const map = new Map<string, LocationGroup<T>>();
  const order: string[] = [];
  for (const item of items) {
    const label = locationLabel(item);
    const key = label.toLowerCase();
    let group = map.get(key);
    if (!group) {
      group = { key, label, items: [] };
      map.set(key, group);
      order.push(key);
    }
    group.items.push(item);
  }
  return order.map((k) => map.get(k)!);
}

export function sourceHost(url: string): string {
  try {
    return new URL(url).hostname.replace(/^www\./, "");
  } catch {
    return "";
  }
}

export type SimilarGroup = {
  key: string;
  label: string;
  lots: Lot[];
};

export function normalizeLotText(s: string): string {
  return s
    .toLowerCase()
    .replace(/lot\s*#?\s*\d+/gi, " ")
    .replace(/[^a-z0-9]+/g, " ")
    .replace(/\s+/g, " ")
    .trim();
}

export function lotGroupKey(
  lot: Pick<Lot, "id" | "modelOrSku" | "identification" | "title">,
): string {
  const model = normalizeLotText(lot.modelOrSku);
  if (model.length >= 2) return `model:${model}`;
  const ident = normalizeLotText(lot.identification);
  if (ident.length >= 8) return `text:${ident}`;
  const title = normalizeLotText(lot.title);
  if (title.length >= 8) return `text:${title}`;
  return `id:${lot.id}`;
}

export function groupSimilarLots(lots: Lot[]): SimilarGroup[] {
  const map = new Map<string, Lot[]>();
  const order: string[] = [];
  for (const lot of lots) {
    const key = lotGroupKey(lot);
    const bucket = map.get(key);
    if (!bucket) {
      map.set(key, [lot]);
      order.push(key);
    } else {
      bucket.push(lot);
    }
  }
  return order.map((key) => {
    const items = [...(map.get(key) ?? [])].sort(compareSimilarLots);
    const head = items[0];
    return {
      key,
      label: head ? similarGroupLabel(head) : key,
      lots: items,
    };
  });
}

function similarGroupLabel(lot: Lot): string {
  if (lot.identification && lot.identification !== lot.title) return lot.identification;
  if (lot.modelOrSku) return lot.modelOrSku;
  return lot.title || lot.id;
}

function compareSimilarLots(a: Lot, b: Lot): number {
  const as = a.dealScore ?? -1;
  const bs = b.dealScore ?? -1;
  if (as !== bs) return bs - as;
  const ab = a.currentBidCents ?? Number.POSITIVE_INFINITY;
  const bb = b.currentBidCents ?? Number.POSITIVE_INFINITY;
  if (ab !== bb) return ab - bb;
  return (a.endsAt || "").localeCompare(b.endsAt || "");
}

export type SortDir = "asc" | "desc";

export type SortState<C extends string> = {
  column: C;
  dir: SortDir;
};

export type LotSortColumn =
  | "lot"
  | "name"
  | "bid"
  | "ends"
  | "category"
  | "location"
  | "price"
  | "gap"
  | "bucket"
  | "saved"
  | "why";

export type AuctionSortColumn = "title" | "status" | "lots" | "ends";

export type SitesSortColumn = "title" | "lots" | "ends";

export const LOT_SORT_DEFAULTS: Record<LotSortColumn, SortDir> = {
  lot: "asc",
  name: "asc",
  bid: "asc",
  ends: "asc",
  category: "asc",
  location: "asc",
  price: "desc",
  saved: "desc",
  gap: "desc",
  bucket: "asc",
  why: "asc",
};

export const AUCTION_SORT_DEFAULTS: Record<AuctionSortColumn, SortDir> = {
  title: "asc",
  status: "asc",
  lots: "desc",
  ends: "asc",
};

export const SITES_SORT_DEFAULTS: Record<SitesSortColumn, SortDir> = {
  title: "asc",
  lots: "desc",
  ends: "asc",
};

/** First click uses the column default, second reverses, third clears. */
export function cycleSort<C extends string>(
  current: SortState<C> | null,
  column: C,
  defaultDir: SortDir,
): SortState<C> | null {
  if (current?.column !== column) {
    return { column, dir: defaultDir };
  }
  if (current.dir === defaultDir) {
    return { column, dir: defaultDir === "asc" ? "desc" : "asc" };
  }
  return null;
}

type SortValue = { empty: true } | { empty: false; text?: string; number?: number };

function textValue(s: string | undefined): SortValue {
  const value = (s ?? "").trim();
  return value === "" ? { empty: true } : { empty: false, text: value };
}

function numberValue(n: number | null | undefined): SortValue {
  return n == null || Number.isNaN(n) ? { empty: true } : { empty: false, number: n };
}

function compareSortValues(a: SortValue, b: SortValue, dir: SortDir): number {
  if (a.empty || b.empty) {
    if (a.empty && b.empty) return 0;
    return a.empty ? 1 : -1;
  }
  const inner =
    a.text != null && b.text != null
      ? a.text.localeCompare(b.text, undefined, { numeric: true, sensitivity: "base" })
      : a.number != null && b.number != null
        ? a.number - b.number
        : 0;
  return dir === "asc" ? inner : -inner;
}

function lotSortValue(lot: Lot, column: LotSortColumn): SortValue {
  switch (column) {
    case "lot":
      return textValue(lot.lotCode);
    case "name":
      return textValue(lot.title || lot.id);
    case "bid":
      return numberValue(lot.currentBidCents);
    case "ends":
      return textValue(lot.endsAt);
    case "category":
      return textValue(lot.category);
    case "location":
      return textValue(locationLabelOrEmpty(lot));
    case "price":
      return numberValue(lot.priceCents);
    case "gap":
      return numberValue(lot.dealScore);
    case "bucket":
      return textValue(lot.bucket);
    case "saved":
      return textValue(lot.savedAt);
    case "why":
      return textValue(lot.matchReason);
  }
}

function compareLots(a: Lot, b: Lot, sort: SortState<LotSortColumn>): number {
  const n = compareSortValues(lotSortValue(a, sort.column), lotSortValue(b, sort.column), sort.dir);
  return n !== 0 ? n : a.id.localeCompare(b.id, undefined, { numeric: true });
}

export function sortLots(lots: Lot[], sort: SortState<LotSortColumn> | null): Lot[] {
  if (!sort) return lots;
  return [...lots].sort((a, b) => compareLots(a, b, sort));
}

export function sortLotGroups(groups: SimilarGroup[], sort: SortState<LotSortColumn> | null): SimilarGroup[] {
  if (!sort) return groups;
  return groups
    .map((group) => ({ ...group, lots: sortLots(group.lots, sort) }))
    .sort((a, b) => {
      const left = a.lots[0];
      const right = b.lots[0];
      if (!left && !right) return 0;
      if (!left) return 1;
      if (!right) return -1;
      return compareLots(left, right, sort);
    });
}

function auctionSortValue(auction: Auction, column: AuctionSortColumn): SortValue {
  switch (column) {
    case "title":
      return textValue(auction.title || auction.id);
    case "status":
      return textValue(auction.status);
    case "lots":
      return numberValue(auction.lotCount);
    case "ends":
      return textValue(auction.endsAt);
  }
}

export function sortAuctions(auctions: Auction[], sort: SortState<AuctionSortColumn> | null): Auction[] {
  if (!sort) return auctions;
  return [...auctions].sort((a, b) => {
    const n = compareSortValues(
      auctionSortValue(a, sort.column),
      auctionSortValue(b, sort.column),
      sort.dir,
    );
    return n !== 0 ? n : a.id.localeCompare(b.id, undefined, { numeric: true });
  });
}

function sitesSortValue(auction: SitesAuction, column: SitesSortColumn): SortValue {
  switch (column) {
    case "title":
      return textValue(auction.title || auction.id);
    case "lots":
      return numberValue(auction.itemCount);
    case "ends":
      return textValue(auction.endsAt);
  }
}

export function sortSitesAuctions(
  auctions: SitesAuction[],
  sort: SortState<SitesSortColumn> | null,
): SitesAuction[] {
  if (!sort) return auctions;
  return [...auctions].sort((a, b) => {
    const n = compareSortValues(sitesSortValue(a, sort.column), sitesSortValue(b, sort.column), sort.dir);
    return n !== 0 ? n : a.id.localeCompare(b.id, undefined, { numeric: true });
  });
}

export type CleanupResult = {
  auctions: number;
  lots: number;
  sites: number;
  hidden: number;
  kept: number;
};

export function cleanupMessage(result: CleanupResult): string {
  const parts: string[] = [];
  if (result.auctions > 0) {
    parts.push(`${result.auctions} ended auction${result.auctions === 1 ? "" : "s"}`);
  }
  if (result.lots > 0) {
    parts.push(`${result.lots} ended lot${result.lots === 1 ? "" : "s"}`);
  }
  if (result.sites > 0) {
    parts.push(`${result.sites} ended SITES listing${result.sites === 1 ? "" : "s"}`);
  }
  // Saying what was kept matters more than saying what went: a tidy that silently
  // spared your saved lots looks identical to one that quietly deleted them. An ended
  // auction held back by something saved is hidden rather than removed, so say that
  // too — otherwise it reads as an auction the tidy failed to take.
  const kept = result.kept > 0 ? ` Kept ${result.kept} you saved.` : "";
  const hidden =
    result.hidden > 0
      ? ` Hid ${result.hidden} ended auction${result.hidden === 1 ? "" : "s"} still holding something saved.`
      : "";
  if (parts.length === 0) {
    const nothing = kept || hidden ? "Nothing had ended that you had not saved." : "Nothing had ended.";
    return `${nothing}${kept}${hidden}`;
  }
  return `Removed ${parts.join(" and ")}.${kept}${hidden}`;
}

/**
 * Where a lot sits among its auction's lots, and what is either side of it. The list is
 * taken in the order the auction screen shows it, so "next" means the next row there.
 */
export function lotNeighbours<T extends { id: string }>(
  lots: T[],
  id: string,
): { prev: T | null; next: T | null; position: string } {
  const at = lots.findIndex((lot) => lot.id === id);
  if (at < 0) return { prev: null, next: null, position: "" };
  return {
    prev: lots[at - 1] ?? null,
    next: lots[at + 1] ?? null,
    position: `${at + 1} of ${lots.length}`,
  };
}

/**
 * A single broadcast bid, folded into the row a screen already has.
 *
 * Every other bidrl event -- a collection finishing, a scan, a catalog refresh that
 * moved a hundred lots -- is an invalidation, and returning undefined refetches. This
 * only shortcuts the one case where the event carries the whole change: one lot, one
 * new price, arriving several times a minute while an auction closes.
 */
export type BidObserved = {
  lotId: string;
  currentBidCents?: number;
  minBidCents?: number;
  bidIncrementCents?: number;
  bidCount?: number;
  highBidder?: string;
  endsAt?: string;
  biddingExtended?: boolean;
  reserveMet?: boolean;
};

/** Folds one live bid into a lot. */
function withBid(lot: Lot, bid: BidObserved): Lot {
  return {
    ...lot,
    currentBidCents: bid.currentBidCents ?? lot.currentBidCents,
    minBidCents: bid.minBidCents ?? lot.minBidCents,
    bidIncrementCents: bid.bidIncrementCents ?? lot.bidIncrementCents,
    bidCount: bid.bidCount ?? lot.bidCount,
    highBidder: bid.highBidder ?? lot.highBidder,
    endsAt: bid.endsAt || lot.endsAt,
    biddingExtended: bid.biddingExtended ?? lot.biddingExtended,
    reserveMet: bid.reserveMet ?? lot.reserveMet,
  };
}

/** Live bids keyed by lot, newest wins. */
export type BidOverlay = Readonly<Record<string, BidObserved>>;

/**
 * Lays live bids over a page of lots.
 *
 * The overlay is not a second source of truth: every bid here was written to the
 * database before it was published, so a refetch produces the same numbers. It exists
 * so a screen can show one price moving without refetching the list it is already
 * showing to learn it.
 */
export function overlayBids(lots: readonly Lot[], bids: BidOverlay): Lot[] {
  return lots.map((lot) => {
    const bid = bids[lot.id];
    return bid ? withBid(lot, bid) : lot;
  }) as Lot[];
}

/** Lays live bids over a single lot. */
export function overlayBid(lot: Lot, bids: BidOverlay): Lot {
  const bid = bids[lot.id];
  return bid ? withBid(lot, bid) : lot;
}
