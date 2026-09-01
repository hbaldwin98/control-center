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
