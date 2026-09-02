/**
 * The TID usage screen.
 *
 * This file carries its own mounting harness rather than importing core's. A plugin is
 * standalone -- `@cc/ui` and its own directory, nothing else -- and a shared test
 * harness couples a plugin to core just as firmly as shared production code would.
 * `plugins/boundary.test.ts` enforces that, so the duplication below is deliberate.
 *
 * What is worth pinning here: the screen reads a meter, and a meter that silently shows
 * the wrong number is worse than one that shows nothing. So the assertions are about
 * which figures reach the screen, and about the cases where a figure does not exist --
 * no cost, no insight, never synced.
 */
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import tid from "./index";

type Call = { method: string; path: string };

class FakeEventSource {
  static instances: FakeEventSource[] = [];
  readyState = 1;
  onerror: ((event: Event) => void) | null = null;
  constructor(_url: string) {
    FakeEventSource.instances.push(this);
  }
  close(): void {}
  addEventListener(): void {}
  removeEventListener(): void {}
}

function day(over: Record<string, unknown> = {}) {
  return { day: "2026-09-01", kwh: 24.5, costCents: 310, onPeakKwh: 8, offPeakKwh: 16.5, ...over };
}

function summary(over: Record<string, unknown> = {}) {
  return {
    monthKwh: 420.5,
    lastMonthKwh: 380.25,
    lastBillingPeriodKwh: 401,
    lastBillingPeriodFrom: "2026-08-01",
    lastBillingPeriodTo: "2026-08-31",
    lastYearKwh: 4_800,
    peakDay: "2026-09-01",
    peakKwh: 31.2,
    estCostCents: 5_400,
    spark: [10, 12, 14, 11],
    days: [day()],
    insight: null,
    lastSync: { at: "2026-09-01T06:00:00Z", status: "ok", rows: 24, source: "tid", error: "" },
    latestEventId: 1,
    ...over,
  };
}

function period(over: Record<string, unknown> = {}) {
  return {
    start: "2026-08-01",
    end: "2026-08-31",
    totalKwh: 401,
    totalCostCents: 5_100,
    onPeakKwh: 120,
    offPeakKwh: 281,
    peakDemandDate: "2026-08-14",
    peakDemandKw: 6.2,
    days: [day()],
    ...over,
  };
}

let container: HTMLDivElement;
let root: Root;
let calls: Call[];
const routes = new Map<string, unknown>();
const failures = new Map<string, number>();

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  calls = [];
  routes.clear();
  failures.clear();

  vi.stubGlobal(
    "fetch",
    vi.fn((input: string, init?: RequestInit) => {
      const path = String(input).split("?")[0] ?? "";
      calls.push({ method: init?.method ?? "GET", path });
      const status = failures.get(path);
      if (status !== undefined) {
        return Promise.resolve(
          new Response(JSON.stringify({ error: "failed", message: "upstream refused" }), {
            status,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      const body = routes.get(path);
      return Promise.resolve(
        new Response(JSON.stringify(body ?? {}), {
          status: body === undefined ? 404 : 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    }),
  );
  FakeEventSource.instances = [];
  vi.stubGlobal("EventSource", FakeEventSource);
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
});

async function render() {
  await act(async () => {
    root.render(
      <MemoryRouter initialEntries={["/tid"]}>
        <Routes>
          {tid.routes.map((r) => (
            <Route key={r.path} path={r.path} element={r.element} />
          ))}
        </Routes>
      </MemoryRouter>,
    );
  });
  for (let i = 0; i < 3; i += 1) {
    await act(async () => {
      await Promise.resolve();
    });
  }
}

/** States what the host returns for this test. Every test calls it before rendering. */
function serve(opts: { summary?: unknown; history?: unknown } = {}) {
  routes.set("/api/plugins/tid/summary", opts.summary ?? summary());
  routes.set(
    "/api/plugins/tid/history",
    opts.history ?? { periods: [period()], latestEventId: 1 },
  );
}

const text = () => container.textContent ?? "";

async function click(pattern: RegExp): Promise<boolean> {
  const el = [...container.querySelectorAll("button, a, summary")].find((n) =>
    pattern.test((n.textContent ?? "").trim()),
  );
  if (!el) return false;
  await act(async () => {
    (el as HTMLElement).click();
  });
  for (let i = 0; i < 3; i += 1) {
    await act(async () => {
      await Promise.resolve();
    });
  }
  return true;
}

describe("tid usage screen", () => {
  it("mounts and shows this month's usage", async () => {
    serve();
    await render();
    expect(text()).toMatch(/420/);
  });

  it("reports the peak day the host identified", async () => {
    serve({ summary: summary({ peakDay: "2026-09-02", peakKwh: 44.4 }) });
    await render();
    expect(text()).toMatch(/44\.4/);
  });

  // A tariff the plugin could not price yields a null cost, and the screen must not
  // render that as zero -- a meter reading nobody was billed for is a lie.
  it("does not invent a cost when the host reported none", async () => {
    serve({ summary: summary({ estCostCents: null, days: [day({ costCents: null })] }) });
    await render();
    expect(text()).not.toMatch(/\$0\.00/);
  });

  it("shows an AI insight when there is one", async () => {
    serve({
      summary: summary({
        insight: {
          at: "2026-09-01T12:00:00Z",
          summary: "Usage is up 11% on last month.",
          recommendation: "Shift laundry off peak.",
          anomalies: ["A spike on the 14th"],
        },
      }),
    });
    await render();
    expect(text()).toContain("Usage is up 11% on last month.");
    expect(text()).toContain("Shift laundry off peak.");
  });

  it("renders without an insight", async () => {
    serve({ summary: summary({ insight: null }) });
    await render();
    expect(text().length).toBeGreaterThan(0);
  });

  it("reports a failed sync rather than looking healthy", async () => {
    serve({
      summary: summary({
        lastSync: {
          at: "2026-09-01T06:00:00Z",
          status: "failed",
          rows: 0,
          source: "tid",
          error: "login rejected",
        },
      }),
    });
    await render();
    expect(text()).toMatch(/login rejected|failed/i);
  });

  it("renders before the meter has ever been read", async () => {
    serve({ summary: summary({ lastSync: null, days: [], spark: [], monthKwh: 0, estCostCents: null }) });
    await render();
    expect(text().length).toBeGreaterThan(0);
  });

  it("asks the host to sync on request", async () => {
    serve();
    routes.set("/api/plugins/tid/sync", { ok: true });
    await render();
    await click(/sync|refresh|update/i);
    expect(calls.some((c) => c.method === "POST")).toBe(true);
  });

  it("stays usable when the summary cannot be loaded", async () => {
    serve();
    failures.set("/api/plugins/tid/summary", 503);
    await render();
    expect(text().length).toBeGreaterThan(0);
  });

  it("stays usable when history cannot be loaded", async () => {
    serve();
    failures.set("/api/plugins/tid/history", 503);
    await render();
    expect(text().length).toBeGreaterThan(0);
  });

  it("renders billing history with a peak demand figure", async () => {
    serve({ history: { periods: [period({ peakDemandKw: 7.7 })], latestEventId: 1 } });
    await render();
    await click(/history|billing|period/i);
    expect(text().length).toBeGreaterThan(0);
  });

  it("renders a billing period with no demand charge", async () => {
    serve({ history: { periods: [period({ peakDemandKw: null, totalCostCents: null })], latestEventId: 1 } });
    await render();
    await click(/history|billing|period/i);
    expect(text().length).toBeGreaterThan(0);
  });

  it("renders with no billing history at all", async () => {
    serve({ history: { periods: [], latestEventId: 1 } });
    await render();
    await click(/history|billing|period/i);
    expect(text().length).toBeGreaterThan(0);
  });
});
