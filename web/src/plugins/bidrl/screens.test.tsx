/**
 * Smoke tests for the screens themselves.
 *
 * The pure list logic is covered in `model.test.ts`. What this file is for is the wiring:
 * that each screen mounts, that the router hooks it now depends on resolve, and that its
 * filter state really does travel through the query string. Those are the failures a unit
 * test of a helper cannot see.
 */
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import bidrl from "./index";
import type { Finding, Lot } from "./model";

const WATCHLIST = {
  id: "wl-1",
  name: "Camping",
  query: "camping gear",
  enabled: true,
  affiliateIds: ["19"],
  categories: [] as string[],
  maxBidCents: 8000,
  minScore: 0.55,
  status: "ready",
  lastError: "",
  lastRunAt: "2026-08-31T00:00:00Z",
  createdAt: "2026-08-30T00:00:00Z",
  newFindings: 1,
};

function finding(over: Partial<Finding> = {}): Finding {
  return {
    id: "wl-1-1001",
    watchlistId: "wl-1",
    watchlist: "Camping",
    score: 1.2,
    reason: "a two-burner camp stove, which is camping gear",
    state: "new",
    createdAt: "2026-09-01T00:00:00Z",
    lot: lot(),
    ...over,
  };
}

/** Every field the Go handler emits, so a screen never sees a shape the server cannot send. */
function lot(over: Partial<Lot> = {}): Lot {
  return {
    id: "1001",
    auctionId: "42",
    url: "https://www.bidrl.com/auction/42/lot/1001",
    lotCode: "A1",
    favorite: false,
    favoriteNote: "",
    savedAt: "",
    affiliateId: "19",
    affiliateName: "Turlock",
    city: "Turlock",
    title: "Keurig coffee maker",
    description: "Single serve brewer, box opened",
    currentBidCents: 1500,
    minBidCents: 1500,
    bidIncrementCents: 100,
    bidCount: 2,
    highBidder: "bidder7",
    endsAt: new Date(Date.now() + 3600_000).toISOString(),
    biddingExtended: false,
    reserveMet: true,
    category: "appliances",
    bucket: "priced",
    identification: "Keurig K-Classic",
    basis: "exact_text",
    modelOrSku: "K55",
    titleAgreement: 0.9,
    mislabelScore: 0.1,
    priceCents: 12900,
    priceKind: "sold",
    sourceUrl: "https://www.ebay.com/itm/1",
    citedText: "$129.00",
    sourceTitle: "Keurig K-Classic sold",
    sourceClass: "resale",
    sourceLabel: "eBay",
    reusedFromLotId: "",
    retrievedAt: new Date().toISOString(),
    dealScore: 0.88,
    thumbUrl: "",
    ...over,
  };
}

const routes = new Map<string, unknown>([
  ["/api/plugins/bidrl/lots", { lots: [lot()], latestEventId: 1 }],
  ["/api/plugins/bidrl/overview", {
    stats: { auctions: 1, lots: 3, scanned: 2, unscanned: 1, priced: 1, live: 3, ending: 1 },
    deals: [lot()],
    closing: [lot()],
    latestEventId: 1,
  }],
  ["/api/plugins/bidrl/feed", { filter: "deals", q: "", lots: [lot()], latestEventId: 1 }],
  ["/api/plugins/bidrl/auctions", { auctions: [{ id: "42", title: "Test Warehouse", status: "open", lotCount: 3, url: "", endsAt: "", city: "Turlock", affiliateName: "SITES" }], latestEventId: 1 }],
  ["/api/plugins/bidrl/auctions/42", { auction: { id: "42", title: "Test Warehouse", status: "open", lotCount: 1 }, lots: [lot()], latestEventId: 1 }],
  ["/api/plugins/bidrl/auctions/42/index", { title: "Test Warehouse", lots: [{ id: "1001", lotCode: "A1", title: "Keurig coffee maker" }], latestEventId: 1 }],
  ["/api/plugins/bidrl/lots/1001", { ...lot(), photoUrls: [], latestEventId: 1 }],
  ["/api/plugins/bidrl/intent", { search: null, lots: [], latestEventId: 1 }],
  ["/api/plugins/bidrl/sites/auctions", { auctions: [], latestEventId: 1 }],
  ["/api/plugins/bidrl/lots/1001/favorite", { lotId: "1001", favorite: true, note: "" }],
  ["/api/plugins/bidrl/findings", {
    findings: [finding()],
    watchlists: [WATCHLIST],
    state: "new",
    latestEventId: 1,
  }],
  ["/api/plugins/bidrl/watchlists", { watchlists: [WATCHLIST], latestEventId: 1 }],
  ["/api/plugins/bidrl/automation", {
    automation: {
      enabled: true,
      locations: 2,
      sweepSchedule: "0 */6 * * *",
      matchSchedule: "30 */6 * * *",
      timeZone: "UTC",
      lastSweepAt: "2026-09-01T06:00:00Z",
      lastSweepNote: "collected 2 auctions, 140 lots",
      lastMatchAt: "2026-09-01T06:30:00Z",
      lastMatchNote: "3 findings from 1 watchlists",
      throttledUntil: "",
      throttled: false,
      newFindings: 3,
    },
    latestEventId: 1,
  }],
  ["/api/plugins/bidrl/findings/wl-1-1001/accept", { id: "wl-1-1001", state: "accepted", lotId: "1001" }],
  ["/api/plugins/bidrl/findings/wl-1-1001/reject", { id: "wl-1-1001", state: "rejected", lotId: "1001" }],
  ["/api/plugins/bidrl/favorites", {
    lots: [lot({ id: "2002", title: "Coleman two-burner stove", favorite: true, savedAt: "2026-08-20T00:00:00Z", favoriteNote: "check the regulator" })],
    latestEventId: 1,
  }],
  ["/api/plugins/bidrl/locations", {
    locations: [
      { id: "19", affiliateName: "SITES Turlock", city: "Turlock", lotCount: 3 },
      { id: "7", affiliateName: "SITES Modesto", city: "Modesto", lotCount: 1 },
    ],
    latestEventId: 1,
  }],
]);

let container: HTMLDivElement;
let root: Root;
let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  // React only suppresses its act() warning when the environment opts in.
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  fetchMock = vi.fn((input: string) => {
    const path = String(input).split("?")[0] ?? "";
    const body = routes.get(path);
    return Promise.resolve(
      new Response(JSON.stringify(body ?? {}), {
        status: body === undefined ? 404 : 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  });
  vi.stubGlobal("fetch", fetchMock);
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
});

async function renderAt(path: string) {
  await act(async () => {
    root.render(
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {bidrl.routes.map((r) => (
            <Route key={r.path} path={r.path} element={r.element} />
          ))}
        </Routes>
      </MemoryRouter>,
    );
  });
  // One more tick for the snapshot loads the first render kicked off.
  await act(async () => {
    await Promise.resolve();
  });
}

describe("bidrl screens", () => {
  it.each([
    ["/bidrl", "BIDRL"],
    ["/bidrl/auctions", "Auctions"],
    ["/bidrl/lots", "Lots"],
    ["/bidrl/findings", "Findings"],
    ["/bidrl/watchlists", "Watchlists"],
    ["/bidrl/saved", "Saved"],
    ["/bidrl/intent", "Intent"],
    ["/bidrl/auction/42", "Test Warehouse"],
    ["/bidrl/lot/1001", "Keurig coffee maker"],
  ])("mounts %s", async (path, expected) => {
    await renderAt(path);
    expect(container.textContent).toContain(expected);
  });

  it("shows every section tab on each screen, with the current one marked", async () => {
    await renderAt("/bidrl/lots");
    const tabs = [...container.querySelectorAll(".cc-tabs a")].map((a) => a.textContent);
    expect(tabs).toEqual(["Overview", "Auctions", "Lots", "Findings", "Saved", "Intent"]);
    expect(container.querySelector('.cc-tabs a[aria-current="page"]')?.textContent).toBe("Lots");
  });

  it("keeps a lot's detail screen under the Lots tab rather than blanking the strip", async () => {
    await renderAt("/bidrl/lot/1001");
    expect(container.querySelector('.cc-tabs a[aria-current="page"]')?.textContent).toBe("Lots");
  });

  it("reads the catalog's filters out of the query string", async () => {
    await renderAt("/bidrl/lots?filter=deals&bucket=priced&q=keurig");
    const preset = container.querySelector<HTMLSelectElement>('select[aria-label="Preset"]');
    const bucket = container.querySelector<HTMLSelectElement>('select[aria-label="Bucket"]');
    const find = container.querySelector<HTMLInputElement>('input[aria-label="Lot search"]');
    expect(preset?.value).toBe("deals");
    expect(bucket?.value).toBe("priced");
    expect(find?.value).toBe("keurig");
  });

  it("asks the API for the filters named in the URL", async () => {
    await renderAt("/bidrl/lots?filter=deals&bucket=priced");
    const calls = fetchMock.mock.calls.map((c) => String(c[0]));
    expect(calls.some((url) => url.includes("filter=deals") && url.includes("bucket=priced"))).toBe(true);
  });

  it("shows a lot's auction location on the catalog, so it can be ruled out without opening it", async () => {
    await renderAt("/bidrl/lots");
    expect(container.textContent).toContain("Turlock");
  });

  it("reads several locations out of the query string and asks the API for exactly those", async () => {
    await renderAt("/bidrl/lots?affiliate=19,7");
    const calls = fetchMock.mock.calls.map((c) => String(c[0]));
    expect(calls.some((url) => url.includes("/lots?") && url.includes("affiliate=19%2C7"))).toBe(true);
    const pressed = [...container.querySelectorAll('.bidrl-loc-filter [aria-pressed="true"]')].map(
      (b) => b.textContent,
    );
    expect(pressed).toHaveLength(2);
  });

  it("shows saved lots with the note you left on them", async () => {
    await renderAt("/bidrl/saved");
    expect(container.textContent).toContain("Coleman two-burner stove");
    expect(container.textContent).toContain("check the regulator");
  });

  it("puts a star on every lot in the catalog, pressed only for the ones already saved", async () => {
    await renderAt("/bidrl/lots");
    const stars = [...container.querySelectorAll<HTMLButtonElement>(".bidrl-star")];
    expect(stars.length).toBeGreaterThan(0);
    expect(stars.every((b) => b.getAttribute("aria-pressed") === "false")).toBe(true);
  });

  it("saves a lot by POSTing rather than queueing a job, and reports it straight away", async () => {
    await renderAt("/bidrl/lots");
    const star = container.querySelector<HTMLButtonElement>(".bidrl-star");
    await act(async () => {
      star?.click();
    });
    const calls = fetchMock.mock.calls.map(
      (c) => ({ url: String(c[0]), method: (c[1] as RequestInit | undefined)?.method }),
    );
    expect(
      calls.some((c) => c.url.endsWith("/lots/1001/favorite") && c.method === "POST"),
    ).toBe(true);
    expect(container.querySelector(".bidrl-star")?.getAttribute("aria-pressed")).toBe("true");
  });

  it("shows why a finding surfaced, as the reason rather than a bare score", async () => {
    await renderAt("/bidrl/findings");
    expect(container.textContent).toContain("Camping");
    expect(container.textContent).toContain("a two-burner camp stove, which is camping gear");
  });

  it("records a decision by POSTing rather than queueing a job", async () => {
    await renderAt("/bidrl/findings");
    const accept = [...container.querySelectorAll<HTMLButtonElement>("button")].find(
      (b) => b.textContent === "Accept",
    );
    expect(accept).toBeDefined();
    await act(async () => {
      accept?.click();
    });
    const calls = fetchMock.mock.calls.map(
      (c) => ({ url: String(c[0]), method: (c[1] as RequestInit | undefined)?.method }),
    );
    expect(
      calls.some((c) => c.url.endsWith("/findings/wl-1-1001/accept") && c.method === "POST"),
    ).toBe(true);
  });

  it("keeps the Findings tab current while you are editing watchlists", async () => {
    await renderAt("/bidrl/watchlists");
    expect(container.querySelector('.cc-tabs a[aria-current="page"]')?.textContent).toBe("Findings");
  });

  it("says what a watchlist narrows to without opening a form", async () => {
    await renderAt("/bidrl/watchlists");
    expect(container.textContent).toContain("camping gear");
    expect(container.textContent).toContain("under $80.00");
  });

  it("says what the schedule is doing on the page you open first", async () => {
    await renderAt("/bidrl");
    expect(container.textContent).toContain("On, sweeping 2 locations every six hours.");
    expect(container.textContent).toContain("collected 2 auctions, 140 lots");
  });

  it("offers the overview's counts as links into the catalog that proves them", async () => {
    await renderAt("/bidrl");
    const targets = [...container.querySelectorAll<HTMLAnchorElement>("a.bidrl-stat")].map(
      (a) => a.getAttribute("href"),
    );
    expect(targets).toContain("/bidrl/lots?filter=deals");
    expect(targets).toContain("/bidrl/lots?ending=soon");
  });

  it("routes in-plugin links instead of reloading the document", async () => {
    await renderAt("/bidrl/lots");
    const internal = [...container.querySelectorAll<HTMLAnchorElement>("a[href^='/bidrl']")];
    expect(internal.length).toBeGreaterThan(0);
    for (const a of internal) {
      expect(a.getAttribute("target")).not.toBe("_blank");
    }
  });
});
