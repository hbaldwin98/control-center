/**
 * The shared drawer. A surface owns the open state and what goes inside; the drawer
 * owns the scrim, the close button, Escape handling, and where focus starts.
 */
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Drawer } from "./components";

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
    true;
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
});

async function render(node: ReactNode) {
  await act(async () => {
    root.render(node);
  });
}

describe("Drawer", () => {
  it("renders nothing while closed", async () => {
    await render(
      <Drawer open={false} onClose={() => {}} label="Details">
        <p>Body</p>
      </Drawer>,
    );

    expect(container.querySelector(".cc-drawer")).toBeNull();
  });

  it("renders a labelled dialog and starts focus on the close button", async () => {
    await render(
      <Drawer open onClose={() => {}} label="Details" closeLabel="Close details">
        <p>Body</p>
      </Drawer>,
    );

    const panel = container.querySelector(".cc-drawer__panel");
    expect(panel?.getAttribute("role")).toBe("dialog");
    expect(panel?.getAttribute("aria-label")).toBe("Details");
    const close = container.querySelector(".cc-drawer__close");
    expect(close?.getAttribute("aria-label")).toBe("Close details");
    expect(document.activeElement).toBe(close);
    expect(container.textContent).toContain("Body");
  });

  it("dismisses on the close button, the scrim, and Escape", async () => {
    const onClose = vi.fn();
    await render(
      <Drawer open onClose={onClose} label="Details">
        <p>Body</p>
      </Drawer>,
    );

    await act(async () => {
      container.querySelector<HTMLButtonElement>(".cc-drawer__close")?.click();
    });
    await act(async () => {
      container.querySelector<HTMLElement>(".cc-drawer__scrim")?.click();
    });
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    });

    expect(onClose).toHaveBeenCalledTimes(3);
  });
});
