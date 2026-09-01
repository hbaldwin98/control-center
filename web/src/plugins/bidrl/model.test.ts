import { describe, expect, it } from "vitest";
import { LOT_CATEGORIES, cents, comparableHint, eventBoundary, filterLabel } from "./model";

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
