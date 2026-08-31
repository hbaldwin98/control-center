import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  ApiError,
  PluginDisabledError,
  api,
  pluginApi,
  setCsrfToken,
} from "./api";
import { idAbove, stream } from "./stream";
import type { Event } from "./types";

class FakeEventSource {
  static last: FakeEventSource | undefined;
  url: string;
  withCredentials: boolean;
  private listeners = new Map<string, Set<(ev: MessageEvent<string>) => void>>();

  constructor(url: string, init?: EventSourceInit) {
    this.url = url;
    this.withCredentials = Boolean(init?.withCredentials);
    FakeEventSource.last = this;
  }

  addEventListener(type: string, listener: (ev: MessageEvent<string>) => void) {
    let set = this.listeners.get(type);
    if (!set) {
      set = new Set();
      this.listeners.set(type, set);
    }
    set.add(listener);
  }

  close() {}

  emit(type: string, data: string) {
    const ev = { data } as MessageEvent<string>;
    for (const fn of this.listeners.get(type) ?? []) fn(ev);
  }
}

function event(over: Partial<Event> & Pick<Event, "id" | "type">): Event {
  return {
    source: "core.jobs",
    subject: "",
    payload: {},
    createdAt: "2026-08-30T00:00:00Z",
    ...over,
  };
}

describe("idAbove", () => {
  it("compares decimal int64 ids without using Number", () => {
    expect(idAbove("2", "1")).toBe(true);
    expect(idAbove("1", "1")).toBe(false);
    expect(idAbove("9007199254740993", "9007199254740992")).toBe(true);
  });
});

describe("api.request", () => {
  beforeEach(() => {
    setCsrfToken("csrf-token");
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(null, { status: 204 })),
    );
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    setCsrfToken("");
  });

  it("sends the CSRF token only on mutations", async () => {
    await api.get("/api/jobs");
    await api.post("/api/jobs/1/cancel");

    const fetchMock = fetch as unknown as ReturnType<typeof vi.fn>;
    const getHeaders = fetchMock.mock.calls[0]?.[1]?.headers as Record<string, string>;
    const postHeaders = fetchMock.mock.calls[1]?.[1]?.headers as Record<string, string>;
    expect(getHeaders["X-CSRF-Token"]).toBeUndefined();
    expect(postHeaders["X-CSRF-Token"]).toBe("csrf-token");
    expect(fetchMock.mock.calls[1]?.[1]).toMatchObject({ credentials: "same-origin" });
  });

  it("raises PluginDisabledError only for that envelope on a plugin call", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        new Response(JSON.stringify({ error: { code: "plugin_disabled", message: "off" } }), {
          status: 503,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );
    await expect(pluginApi("hello").post("/work")).rejects.toBeInstanceOf(PluginDisabledError);
    try {
      await api.post("/api/admin/plugins/hello/enable");
      throw new Error("expected ApiError");
    } catch (err) {
      expect(err).toBeInstanceOf(ApiError);
      expect(err).not.toBeInstanceOf(PluginDisabledError);
    }
  });

  it("turns a non-JSON error body into an ApiError", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("nope", { status: 500 })));
    await expect(api.get("/api/jobs")).rejects.toMatchObject({
      name: "ApiError",
      status: 500,
      code: "internal",
    });
  });
});

describe("snapshot buffering", () => {
  beforeEach(() => {
    vi.stubGlobal("EventSource", FakeEventSource);
    stream.start("0");
  });
  afterEach(() => {
    stream.stop();
    FakeEventSource.last = undefined;
    vi.unstubAllGlobals();
  });

  it("returns only matching events strictly above asOfEventId", async () => {
    let finish: ((value: Response) => void) | undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn(
        () =>
          new Promise<Response>((resolve) => {
            finish = resolve;
          }),
      ),
    );

    const pending = api.snapshot<{ n: number }>("/api/jobs", { events: "core.job.*" });

    const src = FakeEventSource.last;
    if (!src) throw new Error("EventSource was not constructed");
    src.emit("event", JSON.stringify(event({ id: "3", type: "core.job.failed" })));
    src.emit("event", JSON.stringify(event({ id: "4", type: "core.job.failed" })));
    src.emit("event", JSON.stringify(event({ id: "5", type: "bidrl.deal_found", source: "bidrl" })));
    src.emit("event", JSON.stringify(event({ id: "6", type: "core.job.succeeded" })));

    finish!(
      new Response(JSON.stringify({ data: { n: 1 }, asOfEventId: "4" }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );

    const snap = await pending;
    expect(snap.data).toEqual({ n: 1 });
    expect(snap.asOfEventId).toBe("4");
    expect(snap.pending.map((e) => e.id)).toEqual(["6"]);
  });
});
