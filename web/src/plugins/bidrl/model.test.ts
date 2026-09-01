import { describe, expect, it } from "vitest";
import {
  LOT_CATEGORIES,
  cents,
  cleanupMessage,
  comparableHint,
  cycleSort,
  eventBoundary,
  LOT_PRESETS,
  endsWithin,
  filterLabel,
  gapTone,
  lotNeighbours,
  groupByLocation,
  hasEnded,
  pct,
  groupSimilarLots,
  locationLabel,
  sortAuctions,
  sortLotGroups,
  sortLots,
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

describe("cycleSort", () => {
  it("sets the default, reverses, then clears", () => {
    const first = cycleSort(null, "bid", "asc");
    expect(first).toEqual({ column: "bid", dir: "asc" });
    const second = cycleSort(first, "bid", "asc");
    expect(second).toEqual({ column: "bid", dir: "desc" });
    expect(cycleSort(second, "bid", "asc")).toBeNull();
  });

  it("starts a new column from that column's default", () => {
    expect(cycleSort({ column: "bid", dir: "asc" }, "price", "desc")).toEqual({
      column: "price",
      dir: "desc",
    });
  });
});

describe("sortLots", () => {
  it("orders lot codes numerically and keeps blanks last", () => {
    const sorted = sortLots(
      [
        fakeLot({ id: "1", lotCode: "A10" }),
        fakeLot({ id: "2", lotCode: "" }),
        fakeLot({ id: "3", lotCode: "A2" }),
      ],
      { column: "lot", dir: "asc" },
    );
    expect(sorted.map((l) => l.id)).toEqual(["3", "1", "2"]);
  });

  it("sorts names, bids, prices, and expirations", () => {
    const lots = [
      fakeLot({
        id: "cheap",
        title: "Zebra",
        currentBidCents: 100,
        priceCents: 500,
        endsAt: "2026-09-03T00:00:00Z",
      }),
      fakeLot({
        id: "dear",
        title: "Apple",
        currentBidCents: 900,
        priceCents: 4000,
        endsAt: "2026-09-01T00:00:00Z",
      }),
      fakeLot({ id: "open", title: "Mango" }),
    ];
    expect(sortLots(lots, { column: "name", dir: "asc" }).map((l) => l.id)).toEqual([
      "dear",
      "open",
      "cheap",
    ]);
    expect(sortLots(lots, { column: "bid", dir: "asc" }).map((l) => l.id)).toEqual([
      "cheap",
      "dear",
      "open",
    ]);
    expect(sortLots(lots, { column: "price", dir: "desc" }).map((l) => l.id)).toEqual([
      "dear",
      "cheap",
      "open",
    ]);
    expect(sortLots(lots, { column: "ends", dir: "asc" }).map((l) => l.id)).toEqual([
      "dear",
      "cheap",
      "open",
    ]);
  });

  it("leaves missing values last when the direction is reversed", () => {
    const sorted = sortLots(
      [
        fakeLot({ id: "a", currentBidCents: 100 }),
        fakeLot({ id: "b" }),
        fakeLot({ id: "c", currentBidCents: 300 }),
      ],
      { column: "bid", dir: "desc" },
    );
    expect(sorted.map((l) => l.id)).toEqual(["c", "a", "b"]);
  });
});

describe("sortLotGroups", () => {
  it("reorders groups by the sorted representative", () => {
    const groups = groupSimilarLots([
      fakeLot({ id: "k1", title: "Keurig K-Supreme Plus", modelOrSku: "K-Supreme", currentBidCents: 2000 }),
      fakeLot({ id: "k2", title: "Keurig", modelOrSku: "K-Supreme", currentBidCents: 800 }),
      fakeLot({ id: "chair", title: "Office mesh chair", currentBidCents: 400 }),
    ]);
    const sorted = sortLotGroups(groups, { column: "bid", dir: "asc" });
    expect(sorted.map((g) => g.lots[0]?.id)).toEqual(["chair", "k2"]);
    expect(sorted[1]?.lots.map((l) => l.id)).toEqual(["k2", "k1"]);
  });
});

describe("sortAuctions", () => {
  it("orders collected auctions by lot count and close time", () => {
    const auctions = [
      {
        id: "1",
        url: "",
        title: "B",
        status: "open",
        lotCount: 2,
        lastError: "",
        collectedAt: "",
        endsAt: "2026-09-03T00:00:00Z",
        affiliateName: "",
        city: "",
      },
      {
        id: "2",
        url: "",
        title: "A",
        status: "closed",
        lotCount: 9,
        lastError: "",
        collectedAt: "",
        endsAt: "2026-09-01T00:00:00Z",
        affiliateName: "",
        city: "",
      },
    ];
    expect(sortAuctions(auctions, { column: "title", dir: "asc" }).map((a) => a.id)).toEqual(["2", "1"]);
    expect(sortAuctions(auctions, { column: "lots", dir: "desc" }).map((a) => a.id)).toEqual(["2", "1"]);
    expect(sortAuctions(auctions, { column: "ends", dir: "asc" }).map((a) => a.id)).toEqual(["2", "1"]);
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

describe("gapTone", () => {
  it("stays quiet until the gap is wide enough to be worth acting on", () => {
    expect(gapTone(null)).toBe("neutral");
    expect(gapTone(0.05)).toBe("neutral");
    expect(gapTone(0.2)).toBe("warn");
    expect(gapTone(0.5)).toBe("ok");
    expect(gapTone(0.91)).toBe("ok");
  });
});

describe("pct", () => {
  it("rounds a score to a whole percent", () => {
    expect(pct(0.426)).toBe("43%");
    expect(pct(0)).toBe("0%");
    expect(pct(null)).toBe("");
  });
});

describe("hasEnded", () => {
  const now = Date.parse("2026-01-02T00:00:00Z");

  it("is true only once the close time has passed", () => {
    expect(hasEnded("2026-01-01T00:00:00Z", now)).toBe(true);
    expect(hasEnded("2026-01-03T00:00:00Z", now)).toBe(false);
  });

  it("treats a missing or unparsable time as still open", () => {
    expect(hasEnded("", now)).toBe(false);
    expect(hasEnded("soon", now)).toBe(false);
  });
});

function lotAt(id: string, over: Partial<Lot> = {}): Lot {
  return { ...({} as Lot), id, title: id, bucket: "pending", endsAt: "", ...over };
}

describe("filterLabel", () => {
  it("describes every preset the catalog offers", () => {
    for (const preset of LOT_PRESETS) {
      expect(filterLabel(preset.value)).toBe(preset.hint);
    }
  });

  it("falls back to the all-lots description for an unknown preset", () => {
    expect(filterLabel("nonsense")).toBe(LOT_PRESETS[0].hint);
  });
});

describe("endsWithin", () => {
  const now = Date.parse("2026-01-02T00:00:00Z");
  const day = 24 * 3600_000;

  it("counts only close times ahead of now and inside the window", () => {
    expect(endsWithin("2026-01-02T06:00:00Z", now, day)).toBe(true);
    expect(endsWithin("2026-01-04T00:00:00Z", now, day)).toBe(false);
    expect(endsWithin("2026-01-01T00:00:00Z", now, day)).toBe(false);
    expect(endsWithin("", now, day)).toBe(false);
  });
});

describe("lotNeighbours", () => {
  const lots = [lotAt("a"), lotAt("b"), lotAt("c")];

  it("reports what is either side and where the lot sits", () => {
    expect(lotNeighbours(lots, "b")).toMatchObject({ position: "2 of 3" });
    expect(lotNeighbours(lots, "b").prev?.id).toBe("a");
    expect(lotNeighbours(lots, "b").next?.id).toBe("c");
  });

  it("has no neighbour past either end", () => {
    expect(lotNeighbours(lots, "a").prev).toBeNull();
    expect(lotNeighbours(lots, "c").next).toBeNull();
  });

  it("reports nothing for a lot that is not in the list", () => {
    expect(lotNeighbours(lots, "zz")).toEqual({ prev: null, next: null, position: "" });
  });
});
