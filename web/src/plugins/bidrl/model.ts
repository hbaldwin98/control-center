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

export function sourceHost(url: string): string {
  try {
    return new URL(url).hostname.replace(/^www\./, "");
  } catch {
    return "";
  }
}
