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
import { LotBrowser } from "./lots";
import { useLiveBids } from "./live";
import type { Finding, Lot } from "./model";

function LiveProbe({ lots }: { lots: Lot[] }) {
  const live = useLiveBids(lots);
  return <span>{live.status}</span>;
}

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
  [
    "/api/plugins/bidrl/overview",
    {
      stats: {
        auctions: 1,
        lots: 3,
        scanned: 2,
        unscanned: 1,
        priced: 1,
        live: 3,
        ending: 1,
      },
      deals: [lot()],
      closing: [lot()],
      latestEventId: 1,
    },
  ],
  [
    "/api/plugins/bidrl/feed",
    { filter: "deals", q: "", lots: [lot()], latestEventId: 1 },
  ],
  [
    "/api/plugins/bidrl/auctions",
    {
      auctions: [
        {
          id: "42",
          title: "Test Warehouse",
          status: "open",
          lotCount: 3,
          url: "",
          endsAt: "",
          city: "Turlock",
          affiliateName: "SITES",
        },
      ],
      latestEventId: 1,
    },
  ],
  [
    "/api/plugins/bidrl/auctions/42",
    {
      auction: {
        id: "42",
        title: "Test Warehouse",
        status: "open",
        lotCount: 1,
        url: "https://www.bidrl.com/auction/42",
      },
      lots: [lot()],
      latestEventId: 1,
    },
  ],
  [
    "/api/plugins/bidrl/auctions/42/index",
    {
      title: "Test Warehouse",
      lots: [{ id: "1001", lotCode: "A1", title: "Keurig coffee maker" }],
      latestEventId: 1,
    },
  ],
  [
    "/api/plugins/bidrl/lots/1001",
    { ...lot(), photoUrls: [], latestEventId: 1 },
  ],
  ["/api/plugins/bidrl/intent", { search: null, lots: [], latestEventId: 1 }],
  ["/api/plugins/bidrl/sites/auctions", { auctions: [], latestEventId: 1 }],
  [
    "/api/plugins/bidrl/lots/1001/favorite",
    { lotId: "1001", favorite: true, note: "" },
  ],
  ["/api/plugins/bidrl/lots/2002/favorite", { lotId: "2002", favorite: false }],
  [
    "/api/plugins/bidrl/findings",
    {
      findings: [finding()],
      watchlists: [WATCHLIST],
      state: "new",
      latestEventId: 1,
    },
  ],
  [
    "/api/plugins/bidrl/watchlists",
    { watchlists: [WATCHLIST], latestEventId: 1 },
  ],
  [
    "/api/plugins/bidrl/automation",
    {
      automation: {
        enabled: true,
        locations: 2,
        affiliateIds: ["19", "7"],
        maxAuctionsPerSweep: 5,
        maxNewLotsPerSweep: 400,
        queuedAuctions: 3,
        watchlists: 1,
        enabledWatchlists: 1,
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
    },
  ],
  [
    "/api/plugins/bidrl/findings/wl-1-1001/accept",
    { id: "wl-1-1001", state: "accepted", lotId: "1001" },
  ],
  [
    "/api/plugins/bidrl/findings/wl-1-1001/reject",
    { id: "wl-1-1001", state: "rejected", lotId: "1001" },
  ],
  [
    "/api/plugins/bidrl/favorites",
    {
      lots: [
        lot({
          id: "2002",
          title: "Coleman two-burner stove",
          favorite: true,
          savedAt: "2026-08-20T00:00:00Z",
          favoriteNote: "check the regulator",
        }),
      ],
      latestEventId: 1,
    },
  ],
  [
    "/api/plugins/bidrl/locations",
    {
      locations: [
        {
          id: "19",
          affiliateName: "SITES Turlock",
          city: "Turlock",
          lotCount: 3,
        },
        {
          id: "7",
          affiliateName: "SITES Modesto",
          city: "Modesto",
          lotCount: 1,
        },
      ],
      latestEventId: 1,
    },
  ],
]);

/** Enough of EventSource to see which feeds a screen opens, and that it closes them. */
class FakeIntersectionObserver {
  static instances: FakeIntersectionObserver[] = [];
  private readonly callback: (
    entries: Array<{ isIntersecting: boolean }>,
  ) => void;

  constructor(callback: (entries: Array<{ isIntersecting: boolean }>) => void) {
    this.callback = callback;
    FakeIntersectionObserver.instances.push(this);
  }

  observe(): void {}
  disconnect(): void {}
  trigger(): void {
    this.callback([{ isIntersecting: true }]);
  }
}

class FakeEventSource {
  static instances: FakeEventSource[] = [];
  static get opened(): string[] {
    return FakeEventSource.instances.map((s) => s.url);
  }
  url: string;
  closed = false;
  /** 0 CONNECTING, 1 OPEN, 2 CLOSED -- the browser's own retry state. */
  readyState = 1;
  onerror: ((ev: Event) => unknown) | null = null;
  #listeners = new Map<string, Set<(event: { data: string }) => void>>();
  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }
  close(): void {
    this.closed = true;
  }
  addEventListener(type: string, fn: () => void): void {
    const set = this.#listeners.get(type) ?? new Set();
    set.add(fn);
    this.#listeners.set(type, set);
  }
  removeEventListener(
    type: string,
    fn: (event: { data: string }) => void,
  ): void {
    this.#listeners.get(type)?.delete(fn);
  }
  /** Delivers one server-sent event of `type`, framed the way the host's push
   *  endpoint frames every message: a topic and the plugin's own payload. */
  emit(type: string, topic = "", data: unknown = {}): void {
    const event = { data: JSON.stringify({ topic, data }) };
    for (const fn of this.#listeners.get(type) ?? []) fn(event);
  }
  /** Drops the connection with the readyState the browser would report: 0 while it
   *  retries on its own, 2 once it has given up. */
  fail(readyState: number): void {
    this.readyState = readyState;
    this.onerror?.(new Event("error"));
  }
}

let container: HTMLDivElement;
let root: Root;
let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  // React only suppresses its act() warning when the environment opts in.
  (
    globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }
  ).IS_REACT_ACT_ENVIRONMENT = true;
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
  FakeEventSource.instances = [];
  vi.stubGlobal("EventSource", FakeEventSource);
  // The place memory is per visit, not per test.
  sessionStorage.clear();
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

/**
 * A second visit: the router only reads `initialEntries` when it mounts, so leaving one
 * screen for another the way a person would means tearing the tree down first.
 */
async function revisit(path: string) {
  act(() => root.unmount());
  container.remove();
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await renderAt(path);
}

describe("bidrl screens", () => {
  it.each([
    ["/bidrl", "Overview"],
    ["/bidrl/auctions", "Auctions"],
    ["/bidrl/lots", "Lots"],
    ["/bidrl/findings", "Findings"],
    ["/bidrl/watchlists", "Watchlists"],
    ["/bidrl/saved", "Saved"],
    ["/bidrl/automation", "Automation"],
    ["/bidrl/intent", "Intent"],
    ["/bidrl/auction/42", "Test Warehouse"],
    ["/bidrl/lot/1001", "Keurig coffee maker"],
  ])("mounts %s", async (path, expected) => {
    await renderAt(path);
    expect(container.textContent).toContain(expected);
  });

  it.each([
    ["/bidrl/lots", 700, false],
    ["/bidrl/lots", 710, true],
    ["/bidrl/lots", 721, true],
    ["/bidrl/saved", 700, false],
    ["/bidrl/saved", 710, true],
    ["/bidrl/saved", 721, true],
  ])(
    "initializes advanced filters for %s at %dpx",
    async (path, width, open) => {
      const previousWidth = window.innerWidth;
      Object.defineProperty(window, "innerWidth", {
        configurable: true,
        value: width,
      });
      try {
        await renderAt(path);
        expect(
          container.querySelector<HTMLDetailsElement>(
            ".bidrl-advanced-filters",
          )?.open,
        ).toBe(open);
      } finally {
        Object.defineProperty(window, "innerWidth", {
          configurable: true,
          value: previousWidth,
        });
      }
    },
  );

  it("puts the current listing ahead of the decision report", async () => {
    await renderAt("/bidrl/lot/1001");
    const current = container.querySelector(".bidrl-lot-current");
    const metrics = container.querySelector(".bidrl-lot-current__metrics");
    const report = container.querySelector(".bidrl-lot-report");
    const source = container.querySelector(".bidrl-lot-current__source a");
    expect(metrics?.textContent).toContain("Identity");
    expect(metrics?.textContent).toContain("Price");
    expect(metrics?.textContent).toContain("Bids");
    expect(metrics?.textContent).toContain("Bid price");
    expect(metrics?.textContent).toContain("Time remaining");
    expect(source?.textContent).toContain("Open on BidRL");
    expect(current?.compareDocumentPosition(report ?? current)).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    );
    expect(report?.textContent).toContain("Comparable");
  });

  it("keeps the item name and photos above the current listing", async () => {
    const path = "/api/plugins/bidrl/lots/1001";
    const before = routes.get(path);
    routes.set(path, {
      ...lot(),
      photoUrls: ["/one.jpg", "/two.jpg"],
      latestEventId: 1,
    });
    try {
      await renderAt("/bidrl/lot/1001");
      const photos = container.querySelector(".bidrl-lot-hero");
      const current = container.querySelector(".bidrl-lot-current");
      expect(container.querySelector("h1")?.textContent).toContain(
        "Keurig coffee maker",
      );
      expect(photos?.compareDocumentPosition(current ?? photos)).toBe(
        Node.DOCUMENT_POSITION_FOLLOWING,
      );
    } finally {
      if (before) routes.set(path, before);
    }
  });

  // The screen exists to answer "what happens next, and what happened last" without
  // opening the job log, so both ticks have to be on it, named and dated.
  it("reports both scheduled ticks and what they last did", async () => {
    await renderAt("/bidrl/automation");
    expect(container.textContent).toContain("Sweep");
    expect(container.textContent).toContain("Match");
    expect(container.textContent).toContain("collected 2 auctions, 140 lots");
    expect(container.textContent).toContain("3 findings from 1 watchlists");
    // The plan, not just the settings: three of nine queued, capped at five a tick.
    expect(container.textContent).toContain("3 auctions next");
    // Locations by name, because "2 locations" leaves the operator guessing which.
    expect(container.textContent).toContain("SITES Turlock");
  });

  // A latch is the one thing on this screen that needs an answer, so it is the only
  // state that offers a button — and clearing it is a person's decision, never a tick's.
  it("shows no resume control while collection is running normally", async () => {
    await renderAt("/bidrl/automation");
    expect(container.textContent).not.toContain("Resume collection");
  });

  it("resumes a latched collection from the screen", async () => {
    const before = routes.get("/api/plugins/bidrl/automation") as {
      automation: Record<string, unknown>;
    };
    routes.set("/api/plugins/bidrl/automation", {
      ...before,
      automation: {
        ...before.automation,
        throttled: true,
        throttledUntil: "2026-09-02T06:00:00Z",
      },
    });
    try {
      await renderAt("/bidrl/automation");
      expect(container.textContent).toContain("Resume collection");
      const button = [...container.querySelectorAll("button")].find(
        (b) => b.textContent === "Resume collection",
      );
      expect(button).toBeDefined();
      await act(async () => {
        button?.click();
      });
      expect(
        fetchMock.mock.calls.some(
          ([url, init]) =>
            String(url) === "/api/plugins/bidrl/automation/resume" &&
            (init as RequestInit | undefined)?.method === "POST",
        ),
      ).toBe(true);
    } finally {
      routes.set("/api/plugins/bidrl/automation", before);
    }
  });

  // The saved and lots screens span auctions, so the connection is opened for what is
  // on screen rather than for an auction. The transport is the host's, so the URL is
  // /api/push/<plugin> and the lots travel as one topic each.
  it.each([
    ["/bidrl/saved", "lot:2002"],
    ["/bidrl/lots", "lot:1001"],
    ["/bidrl/auction/42", "lot:1001"],
    ["/bidrl/lot/1001", "lot:1001"],
  ])("watches the lots %s is showing", async (path, topics) => {
    await renderAt(path);
    const source = FakeEventSource.opened.find((url) =>
      url.includes("/api/push/bidrl"),
    );
    expect(source, "no live connection was opened").toBeDefined();
    expect(new URL(String(source), "http://x").searchParams.get("topics")).toBe(
      topics,
    );
  });

  it("opens no connection when there is nothing on screen", async () => {
    routes.set("/api/plugins/bidrl/lots", { lots: [], latestEventId: 1 });
    try {
      await renderAt("/bidrl/lots");
      expect(
        FakeEventSource.opened.some((url) => url.includes("/api/push/bidrl")),
      ).toBe(false);
    } finally {
      routes.set("/api/plugins/bidrl/lots", {
        lots: [lot()],
        latestEventId: 1,
      });
    }
  });

  it("bounds live bid topics even if a caller hands it a giant result", async () => {
    const lots = Array.from({ length: 600 }, (_, index) =>
      lot({ id: String(index + 1) }),
    );
    await act(async () => {
      root.render(
        <MemoryRouter>
          <LiveProbe lots={lots} />
        </MemoryRouter>,
      );
    });
    const source = FakeEventSource.opened.find((url) =>
      url.includes("/api/push/bidrl"),
    );
    expect(source).toBeDefined();
    const topics =
      new URL(String(source), "http://x").searchParams
        .get("topics")
        ?.split(",") ?? [];
    expect(topics).toHaveLength(100);
  });

  it("loads the next lot page when its scroll sentinel enters view", async () => {
    const first = Array.from({ length: 50 }, (_, index) =>
      lot({ id: `page1-${index}`, title: `First ${index}` }),
    );
    const second = lot({ id: "page2-0", title: "Scrolled lot" });
    fetchMock.mockImplementation((input: string) => {
      const url = String(input);
      const path = url.split("?")[0] ?? "";
      let body = routes.get(path);
      if (path === "/api/plugins/bidrl/lots") {
        body = url.includes("page=2")
          ? {
              lots: [second],
              page: 2,
              perPage: 50,
              total: 51,
              totalPages: 2,
              hasNext: false,
              latestEventId: 1,
            }
          : {
              lots: first,
              page: 1,
              perPage: 50,
              total: 51,
              totalPages: 2,
              hasNext: true,
              latestEventId: 1,
            };
      }
      return Promise.resolve(
        new Response(JSON.stringify(body ?? {}), {
          status: body === undefined ? 404 : 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    });
    vi.stubGlobal("IntersectionObserver", FakeIntersectionObserver);
    await renderAt("/bidrl/lots");
    expect(FakeIntersectionObserver.instances.length).toBeGreaterThan(0);

    await act(async () => {
      FakeIntersectionObserver.instances.at(-1)?.trigger();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(
      fetchMock.mock.calls.some(([url]) => String(url).includes("page=2")),
    ).toBe(true);
    expect(container.textContent).toContain("51 shown");
  });

  // A badge that lies is worse than none, so it says nothing until the host reports
  // the connection is actually open.
  it("shows the live badge only once the host reports the topics are registered", async () => {
    await renderAt("/bidrl/lots");
    const live = FakeEventSource.instances.find((s) =>
      s.url.includes("/api/push/bidrl"),
    );
    expect(live).toBeDefined();
    expect(container.textContent).not.toContain("Live");

    act(() => live?.emit("ready"));
    // The connection being up is not the feed being up: the host says which topics it
    // is actually feeding, and the badge waits for that.
    expect(container.textContent).not.toContain("Live");
    act(() => live?.emit("available", "lot:1001"));
    expect(container.textContent).toContain("Live");

    act(() => live?.emit("closed"));
    expect(container.textContent).not.toContain("Live");
  });

  // The point of the whole path: a bid changes the number on screen without the screen
  // refetching the list it is already showing.
  it("folds a published bid into the price on screen", async () => {
    await renderAt("/bidrl/lots");
    const live = FakeEventSource.instances.find((s) =>
      s.url.includes("/api/push/bidrl"),
    );
    expect(live).toBeDefined();
    act(() => live?.emit("ready"));
    const before = fetchMock.mock.calls.length;

    act(() =>
      live?.emit("message", "lot:1001", {
        lotId: "1001",
        auctionId: "42",
        currentBidCents: 9900,
        bidCount: 12,
      }),
    );
    expect(container.textContent).toContain("99");
    // No refetch: the message carried everything the row needed.
    expect(fetchMock.mock.calls.length).toBe(before);
  });

  // The bug this pins: closing on the first error turned any transient drop into a
  // permanent one, so the feed delivered a message or two and then was gone for good.
  it("lets a dropped connection retry instead of abandoning it", async () => {
    await renderAt("/bidrl/lots");
    const live = FakeEventSource.instances.find((s) =>
      s.url.includes("/api/push/bidrl"),
    );
    expect(live).toBeDefined();
    act(() => live?.emit("ready"));
    act(() => live?.emit("available", "lot:1001"));
    expect(container.textContent).toContain("Live");

    // readyState 0 is CONNECTING: the browser is retrying on its own.
    act(() => live?.fail(0));
    expect(live?.closed).toBe(false);
    expect(container.textContent).not.toContain("Live");

    // And the badge comes back by itself when the retry succeeds.
    act(() => live?.emit("ready"));
    act(() => live?.emit("available", "lot:1001"));
    expect(container.textContent).toContain("Live");
  });

  it("gives up only once the browser has", async () => {
    await renderAt("/bidrl/lots");
    const live = FakeEventSource.instances.find((s) =>
      s.url.includes("/api/push/bidrl"),
    );
    // readyState 2 is CLOSED: retrying is over, so holding the object open buys nothing.
    act(() => live?.fail(2));
    expect(live?.closed).toBe(true);
  });

  // The bug this pins: an upstream feed that failed after the connection was accepted
  // left the badge lit over prices that had stopped moving.
  it("drops the badge when the host says a topic is no longer fed", async () => {
    await renderAt("/bidrl/lots");
    const live = FakeEventSource.instances.find((s) =>
      s.url.includes("/api/push/bidrl"),
    );
    act(() => live?.emit("ready"));
    act(() => live?.emit("available", "lot:1001"));
    expect(container.textContent).toContain("Live");

    act(() => live?.emit("unavailable", "lot:1001"));
    expect(container.textContent).not.toContain("Live");

    // And it comes back when the plugin reconnects, without waiting for a bid.
    act(() => live?.emit("available", "lot:1001"));
    expect(container.textContent).toContain("Live");
  });

  it("closes the connection when the screen goes away", async () => {
    await renderAt("/bidrl/lots");
    const live = FakeEventSource.instances.find((s) =>
      s.url.includes("/api/push/bidrl"),
    );
    expect(live).toBeDefined();
    expect(live?.closed).toBe(false);
    act(() => root.render(<div />));
    expect(live?.closed).toBe(true);
  });

  it("shows every section tab on each screen, with the current one marked", async () => {
    await renderAt("/bidrl/lots");
    const tabs = [...container.querySelectorAll(".cc-tabs a")].map(
      (a) => a.textContent,
    );
    expect(tabs).toEqual([
      "Overview",
      "Lots",
      "Auctions",
      "Sites",
      "Actions",
      "Findings",
      "Saved",
      "Intent",
    ]);
    expect(
      container.querySelector('.cc-tabs a[aria-current="page"]')?.textContent,
    ).toBe("Lots");
  });

  it("keeps a lot's detail screen under the Lots tab rather than blanking the strip", async () => {
    await renderAt("/bidrl/lot/1001");
    expect(
      container.querySelector('.cc-tabs a[aria-current="page"]')?.textContent,
    ).toBe("Lots");
  });

  it("opens a lot with a decision-first hero and evidence sections", async () => {
    const before = routes.get("/api/plugins/bidrl/lots/1001");
    routes.set("/api/plugins/bidrl/lots/1001", {
      ...lot({ photoUrls: ["/api/blobs/lot-photo"] }),
      latestEventId: 1,
    });
    try {
      await renderAt("/bidrl/lot/1001");
      expect(container.querySelector(".bidrl-lot-hero")).not.toBeNull();
      expect(
        container.querySelector(".bidrl-lot-hero__gallery"),
      ).not.toBeNull();
      expect(
        container.querySelector(".bidrl-lot-opportunity")?.textContent,
      ).toContain("88% below");
      expect(
        container.querySelector(".bidrl-lot-detail-grid__aside"),
      ).not.toBeNull();
      expect(container.textContent).toContain("Save lot");
      expect(container.textContent).toContain("Auction details");
      expect(container.textContent).toContain("Comparable evidence");
    } finally {
      routes.set("/api/plugins/bidrl/lots/1001", before);
    }
  });

  // The heading used to hold Scan, Refresh, Delete, and Open on BidRL in one right-hand
  // pile that could not wrap. Jobs live in a full-width command row; the origin link is
  // a crumb, not a fourth button.
  it("keeps auction jobs out of the page heading", async () => {
    await renderAt("/bidrl/auction/42");
    const header = container.querySelector(".cc-page__header");
    const commands = container.querySelector(".bidrl-command");
    expect(header?.textContent).not.toContain("Scan");
    expect(header?.textContent).not.toContain("Refresh bids");
    expect(header?.textContent).not.toContain("Delete");
    expect(header?.textContent).not.toContain("Open on BidRL");
    expect(commands?.textContent).toContain("Scan");
    expect(commands?.textContent).toContain("Refresh bids");
    expect(commands?.textContent).toContain("Delete");
    const origin = [...container.querySelectorAll<HTMLAnchorElement>("a")].find(
      (a) => a.textContent === "Open on BidRL",
    );
    expect(origin?.closest(".bidrl-crumbs")).not.toBeNull();
    expect(origin?.closest(".cc-page__header")).toBeNull();
    expect(origin?.getAttribute("href")).toBe(
      "https://www.bidrl.com/auction/42",
    );
  });

  it("queues a scan from the auction command row", async () => {
    await renderAt("/bidrl/auction/42");
    const scan = [
      ...container.querySelectorAll<HTMLButtonElement>("button"),
    ].find((b) => b.textContent === "Scan");
    expect(scan).toBeDefined();
    await act(async () => {
      scan?.click();
    });
    expect(
      fetchMock.mock.calls.some(
        ([url, init]) =>
          String(url) === "/api/plugins/bidrl/auctions/42/scan" &&
          (init as RequestInit | undefined)?.method === "POST",
      ),
    ).toBe(true);
  });

  it("reads the catalog's filters out of the query string", async () => {
    await renderAt("/bidrl/lots?filter=deals&bucket=priced&q=keurig");
    const preset = container.querySelector<HTMLSelectElement>(
      'select[aria-label="Preset"]',
    );
    const bucket = container.querySelector<HTMLSelectElement>(
      'select[aria-label="Bucket"]',
    );
    const find = container.querySelector<HTMLInputElement>(
      'input[aria-label="Lot search"]',
    );
    expect(preset?.value).toBe("deals");
    expect(bucket?.value).toBe("priced");
    expect(find?.value).toBe("keurig");
  });

  it("asks the API for the filters and meaningful sort named by the URL", async () => {
    await renderAt("/bidrl/lots?filter=deals&bucket=priced");
    const calls = fetchMock.mock.calls.map((c) => String(c[0]));
    expect(
      calls.some(
        (url) =>
          url.includes("filter=deals") &&
          url.includes("bucket=priced") &&
          url.includes("sort=gap.desc"),
      ),
    ).toBe(true);
  });

  it("keeps the old rows visible with an updating indicator while sorting", async () => {
    let releaseSorted: ((response: Response) => void) | null = null;
    const fallback = fetchMock.getMockImplementation() as
      | ((input: string) => Promise<Response>)
      | undefined;
    fetchMock.mockImplementation((input: string) => {
      if (String(input).includes("sort=name.asc")) {
        return new Promise<Response>((resolve) => {
          releaseSorted = resolve;
        });
      }
      return fallback?.(input) ?? Promise.resolve(new Response("{}"));
    });

    await renderAt("/bidrl/lots");
    const sort = container.querySelector<HTMLSelectElement>(
      'select[aria-label="Sort lots"]',
    );
    expect(sort?.value).toBe("lot.asc");
    await act(async () => {
      if (!sort) throw new Error("sort control missing");
      sort.value = "name.asc";
      sort.dispatchEvent(new Event("change", { bubbles: true }));
      await Promise.resolve();
    });

    expect(container.textContent).toContain("Keurig coffee maker");
    expect(container.textContent).toContain("Updating results…");
    expect(container.textContent).not.toContain("No lots collected yet");
    expect(releaseSorted).not.toBeNull();

    await act(async () => {
      releaseSorted?.(
        new Response(
          JSON.stringify({
            lots: [lot({ id: "2002", title: "Sorted coffee maker" })],
            latestEventId: 2,
          }),
          { headers: { "Content-Type": "application/json" } },
        ),
      );
      await Promise.resolve();
    });
    expect(container.textContent).toContain("Sorted coffee maker");
    expect(container.textContent).not.toContain("Updating results…");
  });

  it("shows a lot's auction location on the catalog, so it can be ruled out without opening it", async () => {
    await renderAt("/bidrl/lots");
    expect(container.textContent).toContain("Turlock");
  });

  it("collapses same lots in table view and expands their similar rows", async () => {
    const lots = [
      lot({
        id: "1001",
        lotCode: "1001",
        title: "Matching item",
        identification: "Shared model",
        modelOrSku: "shared",
      }),
      lot({
        id: "1002",
        lotCode: "1002",
        title: "Matching item",
        identification: "Shared model",
        modelOrSku: "shared",
      }),
    ];
    await act(async () => {
      root.render(
        <MemoryRouter>
          <LotBrowser lots={lots} empty="none" view="table" />
        </MemoryRouter>,
      );
    });
    expect(
      container.querySelectorAll(".bidrl-lot-table tbody > tr"),
    ).toHaveLength(1);
    expect(container.textContent).toContain("Opportunity");
    expect(container.textContent).toContain("1 similar");

    const similar = Array.from(container.querySelectorAll("button")).find(
      (button) => button.textContent === "1 similar",
    );
    expect(similar).toBeDefined();
    await act(async () => {
      similar?.click();
      await Promise.resolve();
    });
    expect(
      container.querySelectorAll(".bidrl-lot-table tbody > tr"),
    ).toHaveLength(2);
    expect(container.textContent).toContain("Lot 1001");
    expect(container.textContent).toContain("Lot 1002");
  });

  it("shares one countdown clock across a large lot grid", async () => {
    const timer = vi.spyOn(globalThis, "setInterval");
    const lots = Array.from({ length: 100 }, (_, index) =>
      lot({
        id: String(index + 1),
        title: `Unique lot ${index + 1}`,
        identification: `Unique identification ${index + 1}`,
        modelOrSku: `model-${index + 1}`,
      }),
    );
    try {
      await act(async () => {
        root.render(
          <MemoryRouter>
            <LotBrowser lots={lots} empty="none" view="grid" />
          </MemoryRouter>,
        );
      });
      expect(timer.mock.calls.filter((call) => call[1] === 1_000)).toHaveLength(
        1,
      );
    } finally {
      timer.mockRestore();
    }
  });

  it("lazy-loads lot thumbnails and decodes them asynchronously", async () => {
    const before = routes.get("/api/plugins/bidrl/lots");
    routes.set("/api/plugins/bidrl/lots", {
      lots: [lot({ thumbUrl: "/api/blobs/lot-thumb" })],
      latestEventId: 1,
    });
    try {
      await renderAt("/bidrl/lots");
      const image = container.querySelector<HTMLImageElement>(
        ".bidrl-lot-card__img",
      );
      expect(image?.loading).toBe("lazy");
      expect(image?.decoding).toBe("async");
    } finally {
      routes.set("/api/plugins/bidrl/lots", before);
    }
  });

  it("puts time, location, and chips in a fixed 2×2 on catalog cards", async () => {
    await renderAt("/bidrl/lots");
    const facts = container.querySelector(".bidrl-lot-card__facts");
    expect(facts).not.toBeNull();
    expect(
      facts?.querySelector(".bidrl-lot-card__where")?.textContent,
    ).toContain("Turlock");
    expect(
      facts?.querySelector(".bidrl-lot-card__when")?.textContent,
    ).toBeTruthy();
    expect(facts?.querySelectorAll(".cc-badge")).toHaveLength(2);
  });

  it("reads several locations out of the query string and asks the API for exactly those", async () => {
    await renderAt("/bidrl/lots?affiliate=19,7");
    const calls = fetchMock.mock.calls.map((c) => String(c[0]));
    expect(
      calls.some(
        (url) => url.includes("/lots?") && url.includes("affiliate=19%2C7"),
      ),
    ).toBe(true);
    const pressed = [
      ...container.querySelectorAll('.bidrl-loc-filter [aria-pressed="true"]'),
    ].map((b) => b.textContent);
    expect(pressed).toHaveLength(2);
  });

  it("shows saved lots with the note you left on them", async () => {
    await renderAt("/bidrl/saved");
    expect(container.textContent).toContain("Coleman two-burner stove");
    expect(container.textContent).toContain("check the regulator");
  });

  it("puts a star on every lot in the catalog, pressed only for the ones already saved", async () => {
    await renderAt("/bidrl/lots");
    const stars = [
      ...container.querySelectorAll<HTMLButtonElement>(".bidrl-star"),
    ];
    expect(stars.length).toBeGreaterThan(0);
    expect(stars.every((b) => b.getAttribute("aria-pressed") === "false")).toBe(
      true,
    );
  });

  it("saves a lot by POSTing rather than queueing a job, and reports it straight away", async () => {
    await renderAt("/bidrl/lots");
    const star = container.querySelector<HTMLButtonElement>(".bidrl-star");
    await act(async () => {
      star?.click();
    });
    const calls = fetchMock.mock.calls.map((c) => ({
      url: String(c[0]),
      method: (c[1] as RequestInit | undefined)?.method,
    }));
    expect(
      calls.some(
        (c) => c.url.endsWith("/lots/1001/favorite") && c.method === "POST",
      ),
    ).toBe(true);
    expect(
      container.querySelector(".bidrl-star")?.getAttribute("aria-pressed"),
    ).toBe("true");
  });

  it("shows why a finding surfaced, as the reason rather than a bare score", async () => {
    await renderAt("/bidrl/findings");
    expect(container.textContent).toContain("Camping");
    expect(container.textContent).toContain(
      "a two-burner camp stove, which is camping gear",
    );
  });

  it("records a decision by POSTing rather than queueing a job", async () => {
    await renderAt("/bidrl/findings");
    const accept = [
      ...container.querySelectorAll<HTMLButtonElement>("button"),
    ].find((b) => b.textContent === "Accept");
    expect(accept).toBeDefined();
    await act(async () => {
      accept?.click();
    });
    const calls = fetchMock.mock.calls.map((c) => ({
      url: String(c[0]),
      method: (c[1] as RequestInit | undefined)?.method,
    }));
    expect(
      calls.some(
        (c) =>
          c.url.endsWith("/findings/wl-1-1001/accept") && c.method === "POST",
      ),
    ).toBe(true);
  });

  it("keeps the Findings tab current while you are editing watchlists", async () => {
    await renderAt("/bidrl/watchlists");
    expect(
      container.querySelector('.cc-tabs a[aria-current="page"]')?.textContent,
    ).toBe("Findings");
  });

  it("says what a watchlist narrows to without opening a form", async () => {
    await renderAt("/bidrl/watchlists");
    expect(container.textContent).toContain("camping gear");
    expect(container.textContent).toContain("under $80.00");
  });

  it("says what the schedule is doing on the page you open first", async () => {
    await renderAt("/bidrl");
    expect(container.textContent).toContain(
      "On, sweeping 2 locations every six hours.",
    );
    expect(container.textContent).toContain("collected 2 auctions, 140 lots");
  });

  it("offers the overview's counts as links into the catalog that proves them", async () => {
    await renderAt("/bidrl");
    const targets = [
      ...container.querySelectorAll<HTMLAnchorElement>("a.bidrl-stat"),
    ].map((a) => a.getAttribute("href"));
    expect(targets).toContain("/bidrl/lots?filter=deals");
    expect(targets).toContain("/bidrl/lots?ending=soon");
  });

  // Filters live in the query string, so back restores them -- but a tab click is a
  // fresh navigation, and it used to land on a reset list.
  it("carries the filters a list was left with into its tab link", async () => {
    await renderAt("/bidrl/lots?filter=deals&bucket=priced");
    await revisit("/bidrl/saved");
    const lotsTab = [
      ...container.querySelectorAll<HTMLAnchorElement>(".cc-tabs a"),
    ].find((a) => a.textContent === "Lots");
    expect(lotsTab?.getAttribute("href")).toBe(
      "/bidrl/lots?filter=deals&bucket=priced",
    );
  });

  it("returns a list to the offset it was scrolled to", async () => {
    // The shell's scroller, which is what a screen scrolls inside.
    const main = document.createElement("div");
    main.className = "cc-main";
    Object.defineProperty(main, "scrollTop", {
      get: () => 640,
      configurable: true,
    });
    main.scrollTo = vi.fn();
    document.body.appendChild(main);
    try {
      await renderAt("/bidrl/lots");
      await act(async () => {
        main.dispatchEvent(new Event("scroll"));
        await new Promise((done) => requestAnimationFrame(() => done(null)));
      });
      await revisit("/bidrl/lot/1001");
      await revisit("/bidrl/lots");
      await act(async () => {
        await new Promise((done) => requestAnimationFrame(() => done(null)));
      });
      expect(main.scrollTo).toHaveBeenCalledWith({ top: 640 });
    } finally {
      main.remove();
    }
  });

  // Saving has no event behind it, so the one list defined by saving has to be told.
  it("drops a lot from Saved the moment it is unstarred, without a reload", async () => {
    await renderAt("/bidrl/saved");
    expect(container.textContent).toContain("Coleman two-burner stove");
    const before = routes.get("/api/plugins/bidrl/favorites");
    routes.set("/api/plugins/bidrl/favorites", { lots: [], latestEventId: 2 });
    try {
      const star = container.querySelector<HTMLButtonElement>(".bidrl-star");
      await act(async () => {
        star?.click();
      });
      await act(async () => {
        await Promise.resolve();
      });
      const calls = fetchMock.mock.calls.map((c) => ({
        url: String(c[0]),
        method: (c[1] as RequestInit | undefined)?.method,
      }));
      expect(
        calls.some(
          (c) => c.url.endsWith("/lots/2002/favorite") && c.method === "DELETE",
        ),
      ).toBe(true);
      expect(container.textContent).not.toContain("Coleman two-burner stove");
    } finally {
      routes.set("/api/plugins/bidrl/favorites", before);
    }
  });

  it("routes in-plugin links instead of reloading the document", async () => {
    await renderAt("/bidrl/lots");
    const internal = [
      ...container.querySelectorAll<HTMLAnchorElement>("a[href^='/bidrl']"),
    ];
    expect(internal.length).toBeGreaterThan(0);
    for (const a of internal) {
      expect(a.getAttribute("target")).not.toBe("_blank");
    }
  });
});
