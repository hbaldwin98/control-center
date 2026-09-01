import type { Event } from "@cc/ui";

export type Auction = {
  id: string;
  url: string;
  title: string;
  status: string;
  lotCount: number;
  lastError: string;
  collectedAt: string;
  endsAt: string;
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
  photoUrls?: string[];
  latestEventId?: number;
};

export type LotsPage = {
  lots: Lot[];
  latestEventId: number;
};

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

export function applyFeedEvent(page: FeedPage, _event: Event): FeedPage {
  return page;
}

export function cents(n: number | null | undefined): string {
  if (n == null) return "—";
  return (n / 100).toLocaleString(undefined, { style: "currency", currency: "USD" });
}

export function filterLabel(filter: string): string {
  switch (filter) {
    case "deals":
      return "Priced lots where the current bid is below a search listing that names the model and a price, preferring eBay sold comps.";
    case "mislabeled":
      return "Title and photographs disagree.";
    case "model":
      return "A model number or barcode was read from a photo.";
    case "worth_opening":
      return "Visually interesting, deliberately unpriced.";
    default:
      return "Every scanned lot.";
  }
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
  const ms = Date.parse(endsAt);
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

export function comparableHint(lot: Pick<Lot, "priceKind" | "sourceLabel" | "sourceUrl">): string {
  const kind = lot.priceKind === "sold" ? "sold" : lot.priceKind === "asking" ? "asking" : "";
  const where = lot.sourceLabel || sourceHost(lot.sourceUrl);
  if (kind && where) return `${kind} · ${where}`;
  return where || kind;
}

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
  | "price"
  | "gap"
  | "bucket"
  | "why";

export type AuctionSortColumn = "title" | "status" | "lots" | "ends";

export type SitesSortColumn = "title" | "lots" | "ends";

export const LOT_SORT_DEFAULTS: Record<LotSortColumn, SortDir> = {
  lot: "asc",
  name: "asc",
  bid: "asc",
  ends: "asc",
  category: "asc",
  price: "desc",
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
    case "price":
      return numberValue(lot.priceCents);
    case "gap":
      return numberValue(lot.dealScore);
    case "bucket":
      return textValue(lot.bucket);
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
  if (parts.length === 0) {
    return "Nothing had ended.";
  }
  return `Removed ${parts.join(" and ")}.`;
}
