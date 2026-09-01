import { act } from "react";
import { createElement, useCallback, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useSnapshot } from "./hooks";
import type { Snapshot } from "./types";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

afterEach(() => {
  act(() => {
    root?.unmount();
  });
  host?.remove();
  root = null;
  host = null;
});

function mount(el: ReturnType<typeof createElement>) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => {
    root!.render(el);
  });
}

async function flush() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

function Probe({
  filter,
  load,
}: {
  filter: string;
  load: (filter: string, signal: AbortSignal) => Promise<Snapshot<{ filter: string }>>;
}) {
  const loader = useCallback(
    (signal: AbortSignal) => load(filter, signal),
    [filter, load],
  );
  const snap = useSnapshot(loader);
  const label =
    snap.status === "ready" ? snap.data.filter : snap.status === "error" ? snap.error.message : snap.status;
  return createElement("div", { "data-testid": "snap" }, label);
}

describe("useSnapshot", () => {
  it("reloads when the loader identity changes", async () => {
    const load = vi.fn(async (filter: string) => ({ data: { filter }, asOfEventId: "0" }));

    function Harness() {
      const [filter, setFilter] = useState("deals");
      return createElement(
        "div",
        null,
        createElement("button", { type: "button", onClick: () => setFilter("all") }, "all"),
        createElement(Probe, { filter, load }),
      );
    }

    mount(createElement(Harness));
    await flush();
    expect(host?.querySelector("[data-testid=snap]")?.textContent).toBe("deals");
    expect(load).toHaveBeenCalledTimes(1);

    act(() => {
      host?.querySelector("button")?.click();
    });
    await flush();
    expect(host?.querySelector("[data-testid=snap]")?.textContent).toBe("all");
    expect(load).toHaveBeenCalledTimes(2);
  });
});
