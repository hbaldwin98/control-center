import { describe, expect, it } from "vitest";
import { cents, eventBoundary, filterLabel } from "./model";

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
    expect(filterLabel("deals")).toMatch(/cited market price/);
  });
});
