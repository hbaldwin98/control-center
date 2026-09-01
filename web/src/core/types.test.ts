import { describe, expect, it } from "vitest";
import {
  idlePulse,
  jobsByPlugin,
  needsAttention,
  pulseOf,
  verdictOf,
  type Job,
  type JobState,
  type PluginState,
} from "./types";

function plugin(partial: Partial<PluginState> = {}): PluginState {
  return {
    pluginId: "hello",
    enabled: true,
    automated: false,
    disabledAt: null,
    disabledBy: "",
    disabledReason: "",
    budget: { hourly: 0, daily: 0, monthly: 0, onExceed: "reject" },
    reservedHour: 0,
    committedHour: 0,
    reservedDay: 0,
    committedDay: 0,
    reservedMonth: 0,
    committedMonth: 0,
    accountingFailed: null,
    ...partial,
  };
}

function job(state: JobState, id = 1): Job {
  return {
    id,
    pluginId: "hello",
    name: "tick",
    state,
    attempt: 1,
    maxAttempts: 3,
    progress: 0,
    createdAt: "2026-01-01T00:00:00Z",
  };
}

describe("pulseOf", () => {
  it("treats an empty list as idle", () => {
    expect(pulseOf([])).toEqual(idlePulse);
  });

  it("counts running and waiting work separately", () => {
    expect(pulseOf([job("running", 3), job("pending", 2), job("retry_wait", 1)])).toEqual({
      running: 1,
      waiting: 2,
      failing: false,
    });
  });

  it("is failing only when the newest job failed, died, or is waiting to retry", () => {
    expect(pulseOf([job("failed")]).failing).toBe(true);
    expect(pulseOf([job("dead")]).failing).toBe(true);
    expect(pulseOf([job("retry_wait")]).failing).toBe(true);
    expect(pulseOf([job("succeeded"), job("failed", 0)]).failing).toBe(false);
    expect(pulseOf([job("cancelled")]).failing).toBe(false);
  });
});

describe("jobsByPlugin", () => {
  it("keeps newest-first order inside each plugin", () => {
    const a: Job = { ...job("running", 3), pluginId: "tid" };
    const b: Job = { ...job("failed", 2), pluginId: "hello" };
    const c: Job = { ...job("succeeded", 1), pluginId: "tid" };
    const grouped = jobsByPlugin([a, b, c]);
    expect(grouped.get("tid")?.map((j) => j.id)).toEqual([3, 1]);
    expect(grouped.get("hello")?.map((j) => j.id)).toEqual([2]);
  });
});

describe("verdictOf", () => {
  it("calls a running plugin running, even when older jobs failed", () => {
    expect(verdictOf(plugin(), pulseOf([job("running", 2), job("failed", 1)]))).toBe("running");
  });

  it("does not keep saying failing after a later success", () => {
    expect(verdictOf(plugin(), pulseOf([job("succeeded", 2), job("failed", 1)]))).toBe("ok");
  });

  it("says failing when the newest job failed and nothing is running", () => {
    expect(verdictOf(plugin(), pulseOf([job("failed")]))).toBe("failing");
  });

  it("puts host failures ahead of job state", () => {
    expect(verdictOf(plugin({ accountingFailed: "2026-01-01T00:00:00Z" }), pulseOf([job("running")]))).toBe(
      "accounting",
    );
    expect(verdictOf(plugin({ enabled: false }), pulseOf([job("running")]))).toBe("disabled");
    expect(verdictOf(plugin({ health: { desiredEnabled: true, runtime: "degraded", lastError: "x" } }))).toBe(
      "degraded",
    );
  });

  it("treats running as healthy attention-wise", () => {
    expect(needsAttention("running")).toBe(false);
    expect(needsAttention("ok")).toBe(false);
    expect(needsAttention("failing")).toBe(true);
    expect(needsAttention("disabled")).toBe(true);
  });
});
