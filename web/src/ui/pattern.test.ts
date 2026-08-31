import { describe, expect, it } from "vitest";
import { isValidPattern, matchesPattern } from "./pattern";

describe("matchesPattern", () => {
  const cases: [string, string, boolean][] = [
    ["**", "core.job.failed", true],
    ["**", "a.b", true],
    ["bidrl.**", "bidrl.deal_found", true],
    ["bidrl.**", "bidrl.scan.started", true],
    ["bidrl.**", "core.job.failed", false],
    ["core.job.*", "core.job.failed", true],
    ["core.job.*", "core.job.a.b", false],
    ["**.failed", "core.job.failed", true],
    ["**.failed", "bidrl.scan.failed", true],
    ["**.failed", "core.job.succeeded", false],
    ["core.*.failed", "core.job.failed", true],
    ["core.*.failed", "core.a.b.failed", false],
    ["core.**.failed", "core.a.b.failed", true],
    ["core.**.failed", "core.failed", true],
    ["a.b", "a.b", true],
    ["a.b", "a.b.c", false],
    ["a.b.**", "a.b", true],
  ];

  it.each(cases)("%s matches %s = %s", (pattern, typ, want) => {
    expect(matchesPattern(pattern, typ)).toBe(want);
  });
});

describe("isValidPattern", () => {
  it("rejects empty, uppercase, empty segments, and unknown wildcards", () => {
    for (const bad of ["", "Core.job", "core..job", "core.job-x", "core.*x", "1core.job", "core.***"]) {
      expect(isValidPattern(bad), bad).toBe(false);
    }
  });

  it("accepts ** , literals, and single-segment wildcards", () => {
    for (const good of ["**", "core.**", "*.job.*", "bidrl.deal_found", "a1.b2_c"]) {
      expect(isValidPattern(good), good).toBe(true);
    }
  });
});
