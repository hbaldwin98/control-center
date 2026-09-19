/**
 * The authenticated frame's navigation.
 *
 * `Layout` is the one component every screen renders inside, so these are the wiring
 * tests for the shared chrome: which navigation group a plugin item lands in, that a
 * disabled descriptor actually greys its link, and that a plugin's declared `entry` —
 * not merely its first nav item — is where its live link points.
 */
import { describe, expect, it, vi } from "vitest";
import type { PluginDescriptor, PluginModule } from "@cc/ui";
import { Layout } from "./Layout";
import { setupHarness } from "../core/testing/harness";

vi.mock("./session", () => ({
  useSession: () => ({ logout: vi.fn() }),
}));

vi.mock("../core/AlertSound", () => ({
  useAlertChime: () => {},
}));

const plugins: PluginModule[] = [
  {
    id: "alpha",
    nav: [{ path: "/alpha", label: "Alpha", icon: "sparkle", topLevel: true }],
    routes: [],
  },
  {
    id: "beta",
    nav: [{ path: "/beta", label: "Beta", icon: "eye" }],
    routes: [],
  },
  {
    id: "gamma",
    // The home screen is second; a live link that used nav[0] would go to /gamma/reports.
    nav: [
      { path: "/gamma/reports", label: "Reports", icon: "bolt" },
      { path: "/gamma", label: "Gamma home", icon: "gavel" },
    ],
    entry: "/gamma",
    routes: [],
  },
];

const descriptors: PluginDescriptor[] = [
  { id: "alpha", name: "Alpha Plugin", enabled: true },
  { id: "beta", name: "Beta Plugin", enabled: false },
  { id: "gamma", name: "Gamma Plugin", enabled: true },
];

const h = setupHarness();

function group(label: string): Element | undefined {
  return [...h.container.querySelectorAll(".cc-nav__group")].find(
    (g) => g.querySelector(".cc-nav__section")?.textContent === label,
  );
}

function liveLinkFor(name: string): HTMLAnchorElement | undefined {
  return [...h.container.querySelectorAll<HTMLAnchorElement>(".cc-nav__live-link")].find((a) =>
    (a.textContent ?? "").includes(name),
  );
}

describe("Layout navigation", () => {
  it("puts a topLevel plugin item in the Command group", async () => {
    h.routes.set("/api/admin/plugins", {
      data: [{ pluginId: "beta", enabled: false }],
      asOfEventId: "0",
    });
    await h.render(<Layout plugins={plugins} descriptors={descriptors} />);

    expect(group("Command")?.querySelector('a[href="/alpha"]')?.textContent).toContain("Alpha");
  });

  it("puts a non-topLevel plugin item under Live plugins", async () => {
    h.routes.set("/api/admin/plugins", {
      data: [{ pluginId: "beta", enabled: false }],
      asOfEventId: "0",
    });
    await h.render(<Layout plugins={plugins} descriptors={descriptors} />);

    expect(h.container.querySelector('.cc-nav__live a[href="/beta"]')).not.toBeNull();
    expect(group("Command")?.querySelector('a[href="/beta"]')).toBeNull();
  });

  it("marks a disabled plugin link as off and aria-disabled", async () => {
    h.routes.set("/api/admin/plugins", {
      data: [{ pluginId: "beta", enabled: false }],
      asOfEventId: "0",
    });
    await h.render(<Layout plugins={plugins} descriptors={descriptors} />);

    const link = liveLinkFor("Beta Plugin");
    expect(link?.classList.contains("cc-nav__live-link--off")).toBe(true);
    expect(link?.getAttribute("aria-disabled")).toBe("true");
  });

  it("points the live link at entry rather than the first nav item", async () => {
    h.routes.set("/api/admin/plugins", {
      data: [{ pluginId: "beta", enabled: false }],
      asOfEventId: "0",
    });
    await h.render(<Layout plugins={plugins} descriptors={descriptors} />);

    expect(liveLinkFor("Gamma Plugin")?.getAttribute("href")).toBe("/gamma");
  });
});
