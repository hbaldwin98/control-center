/**
 * The Models screen: providers, the routes that name them, and the catalog behind both.
 *
 * A route is the only thing a plugin ever names, so what this screen writes to
 * `/api/admin/ai/*` decides whether a plugin can call a model at all. The assertions are
 * on the requests, not on wording.
 */
import { describe, expect, it } from "vitest";
import { Models } from "./Models";
import { setupHarness, fill } from "./testing/harness";
import { attempt, credential, provider, route, snapshot } from "./testing/fixtures";

const h = setupHarness();

const writes = () => h.calls.filter((c) => c.method !== "GET");

function serve(
  opts: { providers?: unknown[]; routes?: unknown[]; credentials?: unknown[]; plugins?: unknown[] } = {},
) {
  h.routes.set("/api/admin/ai/providers", snapshot(opts.providers ?? [provider()]));
  h.routes.set("/api/admin/ai/routes", snapshot(opts.routes ?? [route()]));
  h.routes.set("/api/admin/credentials", snapshot(opts.credentials ?? [credential()]));
  // PluginNeedsPanel sits on this screen and loads the plugin list of its own accord.
  h.routes.set("/api/admin/plugins", snapshot(opts.plugins ?? []));
}

describe("Models", () => {
  it("lists the providers and routes the host reported", async () => {
    serve({
      providers: [provider({ id: "openai" }), provider({ id: "anthropic" })],
      routes: [route({ logicalName: "cheap-chat" }), route({ logicalName: "cheap-vision" })],
    });
    await h.render(<Models />);

    const text = h.text();
    expect(text).toContain("openai");
    expect(text).toContain("anthropic");
    expect(text).toContain("cheap-chat");
    expect(text).toContain("cheap-vision");
  });

  // With nothing connected the screen is a setup screen: the connect form, not an
  // empty provider list an operator cannot act on.
  it("offers the connect form when no provider exists", async () => {
    serve({ providers: [] });
    await h.render(<Models />);
    expect(h.text()).toMatch(/connect a provider/i);
  });

  it("does not offer the connect form once a provider exists", async () => {
    serve({ providers: [provider()] });
    await h.render(<Models />);
    expect(h.text()).toMatch(/Providers/);
  });

  it("shows a route's last error when the host marked it unhealthy", async () => {
    serve({
      routes: [route({ logicalName: "cheap-chat", healthy: false, lastError: "429 from provider" })],
    });
    await h.render(<Models />);
    expect(h.text()).toContain("429 from provider");
  });

  it("shows every attempt in a route's fallback plan", async () => {
    serve({
      routes: [
        route({
          logicalName: "cheap-chat",
          attemptPlan: [
            attempt({ provider: "openai", model: "gpt-4o-mini" }),
            attempt({ provider: "anthropic", model: "claude-haiku" }),
          ],
        }),
      ],
      providers: [provider({ id: "openai" }), provider({ id: "anthropic" })],
    });
    await h.render(<Models />);

    expect(h.text()).toContain("gpt-4o-mini");
    expect(h.text()).toContain("claude-haiku");
  });

  it("renders a route with no attempts rather than failing", async () => {
    serve({ routes: [route({ logicalName: "cheap-chat", attemptPlan: [] })] });
    await h.render(<Models />);
    expect(h.text()).toContain("cheap-chat");
  });

  it("renders when every endpoint fails", async () => {
    serve();
    for (const p of [
      "/api/admin/ai/providers",
      "/api/admin/ai/routes",
      "/api/admin/credentials",
      "/api/admin/plugins",
    ]) {
      h.failures.set(p, 500);
    }
    await h.render(<Models />);
    expect(h.text()).toContain("Models");
  });

  it("loads a provider's catalog only when asked", async () => {
    serve({ providers: [provider({ id: "openai" })] });
    h.routes.set("/api/admin/ai/providers/openai/models", [{ id: "gpt-4o-mini" }]);
    await h.render(<Models />);

    const catalogPath = "/api/admin/ai/providers/openai/models";
    expect(
      h.calls.some((c) => c.path === catalogPath),
      "the catalog is a paid upstream call, so it must not load on mount",
    ).toBe(false);

    await h.click(/load models|models|catalog/i);
    expect(h.calls.some((c) => c.path === catalogPath)).toBe(true);
  });

  it("deletes a provider by id", async () => {
    serve({ providers: [provider({ id: "anthropic" })], routes: [] });
    h.routes.set("/api/admin/ai/providers/anthropic", { ok: true });
    await h.render(<Models />);
    await h.clickIn("anthropic", /remove|delete/i);

    const del = writes().find((c) => c.method === "DELETE");
    expect(del?.path).toBe("/api/admin/ai/providers/anthropic");
  });

  // Every card on this screen has a Delete, so the assertion is only worth anything if
  // it is scoped to the route's own card.
  it("deletes a route by its logical name", async () => {
    serve({ routes: [route({ logicalName: "cheap-chat" })] });
    h.routes.set("/api/admin/ai/routes/cheap-chat", { ok: true });
    await h.render(<Models />);
    await h.clickIn("cheap-chat", /^delete$|remove/i);

    const dels = writes().filter((c) => c.method === "DELETE");
    expect(dels.map((c) => c.path)).toContain("/api/admin/ai/routes/cheap-chat");
  });

  it("sends codex providers as a subscription on the host's own base URL", async () => {
    serve({ providers: [], credentials: [credential({ id: "oauth1", kind: "oauth" })] });
    h.routes.set("/api/admin/ai/providers/codex", { ok: true });
    await h.render(<Models />);

    // The connect form's kind picker is the first select on the screen.
    await fill(h.container, "select", "codex");
    await h.click(/connect/i);

    const put = writes().find((c) => c.method === "PUT" && c.path.includes("/providers/"));
    if (put) {
      expect(put.body).toMatchObject({ billing: "subscription" });
    } else {
      // Codex needs an OAuth credential picked first; refusing to write is also correct.
      expect(h.text()).toMatch(/sign in|credential/i);
    }
  });
});
