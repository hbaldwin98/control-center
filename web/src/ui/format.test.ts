import { describe, expect, it } from "vitest";
import { formatRelative, formatUSD } from "./format";

const NOW = 1_700_000_000_000;

describe("formatUSD", () => {
  it("renders micro-USD as dollars", () => {
    expect(formatUSD(1_500_000)).toBe("$1.50");
    expect(formatUSD(0)).toBe("$0.00");
  });

  it("keeps sub-cent precision by default, so a tiny charge is never shown as zero", () => {
    expect(formatUSD(1_200)).toBe("$0.0012");
  });

  it("compacts a nonzero amount below a cent rather than rounding it to $0.00", () => {
    expect(formatUSD(1_200, { compact: true })).toBe("<$0.01");
    expect(formatUSD(0, { compact: true })).toBe("$0.00");
  });
});

describe("formatRelative", () => {
  it("reads the same everywhere", () => {
    expect(formatRelative(NOW, NOW)).toBe("just now");
    expect(formatRelative(NOW - 12_000, NOW)).toBe("12s ago");
    expect(formatRelative(NOW - 5 * 60_000, NOW)).toBe("5m ago");
    expect(formatRelative(NOW - 3 * 3_600_000, NOW)).toBe("3h ago");
    expect(formatRelative(NOW - 2 * 86_400_000, NOW)).toBe("2d ago");
  });

  it("does not run backwards on a clock that is slightly ahead", () => {
    expect(formatRelative(NOW + 5_000, NOW)).toBe("just now");
  });
});
