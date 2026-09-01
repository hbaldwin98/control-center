import { describe, expect, it } from "vitest";
import {
  LOT_CATEGORIES,
  cents,
  cleanupMessage,
  comparableHint,
  eventBoundary,
  filterLabel,
  groupByLocation,
  groupSimilarLots,
  locationLabel,
  type Lot,
} from "./model";

describe("cents", () => {
  it("formats integer cents as USD", () => {
    expect(cents(1500)).toMatch(/15/);
    expect(cents(null)).toBe("—");
  });
});

describe("eventBoundary", () => {
  it("stringifies int64 ids", () => {
    expect(eventBoundary(12)).toBe("12");
    expect(eventBoundary("9223372036854775806")).toBe("9223372036854775806");
  });
});

describe("filterLabel", () => {
  it("describes the deals filter", () => {
    expect(filterLabel("deals")).toMatch(/eBay sold/);
  });
});

describe("comparableHint", () => {
  it("names the sold-or-asking site", () => {
    expect(
      comparableHint({
        priceKind: "sold",
        sourceLabel: "eBay",
        sourceUrl: "https://www.ebay.com/itm/1",
      }),
    ).toBe("sold · eBay");
  });
});

describe("LOT_CATEGORIES", () => {
  it("lists the vision category enum", () => {
    expect(LOT_CATEGORIES).toContain("tools");
    expect(LOT_CATEGORIES).toContain("collectibles");
    expect(LOT_CATEGORIES).toHaveLength(10);
  });
});

describe("cleanupMessage", () => {
  it("names what was removed", () => {
    expect(cleanupMessage({ auctions: 0, lots: 0, sites: 0 })).toBe("Nothing had ended.");
    expect(cleanupMessage({ auctions: 1, lots: 4, sites: 0 })).toBe(
      "Removed 1 ended auction and 4 ended lots.",
    );
  });
});

describe("groupSimilarLots", () => {
  it("collapses the same model and identical titles, but not short unique lots", () => {
    const groups = groupSimilarLots([
      fakeLot({ id: "1", title: "Keurig K-Supreme", modelOrSku: "K-Supreme Plus", currentBidCents: 2000 }),
      fakeLot({ id: "2", title: "Keurig coffee maker", modelOrSku: "K-Supreme Plus", currentBidCents: 800, dealScore: 0.4 }),
      fakeLot({ id: "3", title: "Office mesh chair", identification: "office mesh chair" }),
      fakeLot({ id: "4", title: "Office mesh chair" }),
      fakeLot({ id: "5", title: "Lot" }),
      fakeLot({ id: "6", title: "Lot" }),
    ]);
    expect(groups).toHaveLength(4);
    expect(groups[0]?.lots.map((l) => l.id)).toEqual(["2", "1"]);
    expect(groups[1]?.lots).toHaveLength(2);
    expect(groups[2]?.key).toBe("id:5");
    expect(groups[3]?.key).toBe("id:6");
  });
});

function fakeLot(over: Partial<Lot> & Pick<Lot, "id">): Lot {
  return {
    auctionId: "42",
    url: "",
    lotCode: "",
    title: "",
    description: "",
    currentBidCents: null,
    minBidCents: null,
    bidIncrementCents: null,
    bidCount: 0,
    highBidder: "",
    endsAt: "",
    biddingExtended: false,
    reserveMet: false,
    category: "",
    bucket: "pending",
    identification: "",
    basis: "",
    modelOrSku: "",
    titleAgreement: 0,
    mislabelScore: 0,
    priceCents: null,
    priceKind: "",
    sourceUrl: "",
    citedText: "",
    sourceTitle: "",
    sourceClass: "",
    sourceLabel: "",
    reusedFromLotId: "",
    retrievedAt: "",
    dealScore: null,
    thumbUrl: "",
    ...over,
  };
}

describe("groupByLocation", () => {
  it("keeps insertion order and folds city into the SITES name", () => {
    const groups = groupByLocation([
      { affiliateName: "Turlock", city: "Turlock", title: "Warehouse A" },
      { affiliateName: "Modesto", city: "Modesto", title: "Warehouse B" },
      { affiliateName: "Turlock", city: "Turlock", title: "Warehouse C" },
      { title: "Pasted URL" },
    ]);
    expect(groups.map((g) => g.label)).toEqual(["Turlock", "Modesto", "Other locations"]);
    expect(groups[0]?.items).toHaveLength(2);
    expect(locationLabel({ affiliateName: "SITES Sacramento", city: "Sacramento" })).toBe(
      "SITES Sacramento",
    );
  });
});
