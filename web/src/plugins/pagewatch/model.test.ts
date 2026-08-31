import type { Event } from "@cc/ui";
import { describe, expect, it } from "vitest";
import { applyCheckEvent, eventBoundary, type Check, type ChecksPage } from "./model";

function page(checks: Check[] = []): ChecksPage {
  return {
    targetUrl: "https://example.com/",
    expectedText: "Example Domain",
    schedule: "17 */6 * * *",
    snapshotUrl: "/snapshot",
    checks,
  };
}

function event(id: string, payload: unknown = {}): Event {
  return {
    id,
    type: "pagewatch.check.completed",
    source: "pagewatch",
    subject: "example.com",
    payload,
    createdAt: "2026-08-31T12:00:00Z",
  };
}

describe("eventBoundary", () => {
  it("uses the greatest valid int64 id and ignores malformed persisted ids", () => {
    const checks = [
      { eventId: "9223372036854775806" },
      { eventId: "not-an-id" },
      { eventId: 12 },
    ] as Check[];

    expect(eventBoundary(checks)).toBe("9223372036854775806");
  });
});

describe("applyCheckEvent", () => {
  it("normalizes a sparse event with snapshot defaults", () => {
    const updated = applyCheckEvent(page(), event("42", { status: "changed", summary: "New copy" }));

    expect(updated.checks[0]).toEqual({
      id: "42",
      checkedAt: "2026-08-31T12:00:00Z",
      url: "https://example.com/",
      status: "changed",
      contentHash: "",
      expectedText: "Example Domain",
      expectedFound: false,
      summary: "New copy",
      aiRan: false,
      inputTokens: 0,
      outputTokens: 0,
      costMicroUsd: 0,
      browserMs: 0,
      aiMs: 0,
      jobId: 0,
      eventId: "42",
    });
  });

  it("returns the same snapshot when a replayed event is already present", () => {
    const current = page([{ eventId: "42" } as Check]);

    expect(applyCheckEvent(current, event("42"))).toBe(current);
  });

  it("keeps only the latest 100 checks", () => {
    const checks = Array.from({ length: 100 }, (_, index) => ({ eventId: String(index) }) as Check);

    const updated = applyCheckEvent(page(checks), event("100"));

    expect(updated.checks).toHaveLength(100);
    expect(updated.checks[0]!.eventId).toBe("100");
    expect(updated.checks.at(-1)?.eventId).toBe("98");
  });
});
