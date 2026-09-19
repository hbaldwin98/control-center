import { act, type ReactElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it } from "vitest";
import { NAV_ICON_NAMES, NavIcon } from "./icons";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

function mount(el: ReactElement): HTMLDivElement {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => {
    root!.render(el);
  });
  return host;
}

function unmount() {
  act(() => {
    root?.unmount();
  });
  host?.remove();
  root = null;
  host = null;
}

afterEach(unmount);

describe("NavIcon", () => {
  it("renders an svg for every name in the shared vocabulary", () => {
    for (const name of NAV_ICON_NAMES) {
      const rendered = mount(<NavIcon name={name} />);
      expect(rendered.querySelector("svg"), `no svg for "${name}"`).not.toBeNull();
      unmount();
    }
  });

  it("defaults to 16px", () => {
    const rendered = mount(<NavIcon name="gavel" />);
    const svg = rendered.querySelector("svg");
    expect(svg?.getAttribute("width")).toBe("16");
    expect(svg?.getAttribute("height")).toBe("16");
  });

  it("passes size and className through to the svg", () => {
    const rendered = mount(<NavIcon name="gavel" size={24} className="cc-nav__icon" />);
    const svg = rendered.querySelector("svg");
    expect(svg?.getAttribute("width")).toBe("24");
    expect(svg?.getAttribute("height")).toBe("24");
    expect(svg?.getAttribute("class")).toBe("cc-nav__icon");
    expect(svg?.getAttribute("aria-hidden")).toBe("true");
  });
});
