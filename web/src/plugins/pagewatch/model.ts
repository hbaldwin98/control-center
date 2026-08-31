import type { Event } from "@cc/ui";

export type Check = {
  id: number | string;
  checkedAt: string;
  url: string;
  status: "baseline" | "unchanged" | "changed" | "attention";
  contentHash: string;
  expectedText: string;
  expectedFound: boolean;
  summary: string;
  aiRan: boolean;
  inputTokens: number;
  outputTokens: number;
  costMicroUsd: number;
  browserMs: number;
  aiMs: number;
  jobId: number;
  eventId: number | string;
};

export type ChecksPage = {
  targetUrl: string;
  expectedText: string;
  schedule: string;
  snapshotUrl: string;
  checks: Check[];
};

export function eventBoundary(checks: Check[]): string {
  let latest = 0n;
  for (const check of checks) {
    try {
      const id = BigInt(check.eventId);
      if (id > latest) latest = id;
    } catch {
      // Ignore malformed persisted ids; the next stream reset will reload the snapshot.
    }
  }
  return latest.toString();
}

function orDefault<T>(value: T | null | undefined, fallback: T): T {
  return value ?? fallback;
}

function checkFromEvent(page: ChecksPage, event: Event): Check {
  const payload = event.payload as Partial<Check>;
  return {
    id: event.id,
    checkedAt: orDefault(payload.checkedAt, event.createdAt),
    url: orDefault(payload.url, page.targetUrl),
    status: orDefault(payload.status, "attention"),
    contentHash: orDefault(payload.contentHash, ""),
    expectedText: orDefault(payload.expectedText, page.expectedText),
    expectedFound: orDefault(payload.expectedFound, false),
    summary: orDefault(payload.summary, "Check completed without a summary."),
    aiRan: orDefault(payload.aiRan, false),
    inputTokens: orDefault(payload.inputTokens, 0),
    outputTokens: orDefault(payload.outputTokens, 0),
    costMicroUsd: orDefault(payload.costMicroUsd, 0),
    browserMs: orDefault(payload.browserMs, 0),
    aiMs: orDefault(payload.aiMs, 0),
    jobId: orDefault(payload.jobId, 0),
    eventId: event.id,
  };
}

export function applyCheckEvent(page: ChecksPage, event: Event): ChecksPage {
  if (page.checks.some((check) => String(check.eventId) === event.id)) return page;
  return { ...page, checks: [checkFromEvent(page, event), ...page.checks].slice(0, 100) };
}

export function statusTone(status: Check["status"]): "neutral" | "ok" | "warn" | "danger" {
  if (status === "attention") return "danger";
  if (status === "changed") return "warn";
  if (status === "unchanged") return "ok";
  return "neutral";
}

export function statusLabel(status: Check["status"]): string {
  return status === "attention" ? "needs attention" : status;
}
