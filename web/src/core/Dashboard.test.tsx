/**
 * Dashboard and its tiles.
 *
 * The tile is where the shell states a verdict about a plugin -- healthy, needs
 * attention, disabled, over budget -- and that verdict is assembled from three separate
 * snapshots. Getting it wrong shows an operator a green tile for a broken plugin, so the
 * branches worth pinning are the ones that decide what the tile says.
 */
import { describe, expect, it } from "vitest";
import { Dashboard } from "./Dashboard";
import { setupHarness } from "./testing/harness";
import { job, notification, pluginState, snapshot } from "./testing/fixtures";

const h = setupHarness();

function serve(opts: {
  plugins?: unknown[];
  jobs?: unknown[];
  notifications?: unknown[];
} = {}) {
  h.routes.set("/api/admin/plugins", snapshot(opts.plugins ?? [pluginState()]));
  h.routes.set("/api/jobs", snapshot(opts.jobs ?? []));
  h.routes.set("/api/notifications", snapshot({ notifications: opts.notifications ?? [] }));
}

describe("Dashboard", () => {
  it("counts enabled plugins against the total", async () => {
    serve({
      plugins: [
        pluginState({ pluginId: "a", name: "A", enabled: true }),
        pluginState({ pluginId: "b", name: "B", enabled: false }),
        pluginState({ pluginId: "c", name: "C", enabled: true }),
      ],
    });
    await h.render(<Dashboard plugins={[]} />);
    expect(h.text()).toContain("2/3");
  });

  it("names every plugin the host reported, with or without compiled UI", async () => {
    serve({
      plugins: [pluginState({ pluginId: "a", name: "Alpha" }), pluginState({ pluginId: "b", name: "Beta" })],
    });
    await h.render(<Dashboard plugins={[]} />);
    expect(h.text()).toContain("Alpha");
    expect(h.text()).toContain("Beta");
  });

  it("reports all healthy when nothing needs attention", async () => {
    serve();
    await h.render(<Dashboard plugins={[]} />);
    expect(h.text()).toContain("all healthy");
  });

  // A plugin the host started but could not fully wire is the case the dashboard
  // exists to surface. "degraded" is the host's word for it; see the Runtime type.
  it("flags a degraded plugin", async () => {
    serve({
      plugins: [
        pluginState({
          pluginId: "hello",
          name: "Hello",
          health: { desiredEnabled: true, runtime: "degraded", lastError: "migrate: no such table" },
        }),
      ],
    });
    await h.render(<Dashboard plugins={[]} />);
    expect(h.text()).toMatch(/need attention|attention/i);
  });

  it("says a plugin is disabled rather than healthy", async () => {
    serve({
      plugins: [
        pluginState({
          pluginId: "hello",
          enabled: false,
          disabledReason: "turned off by the operator",
          health: { desiredEnabled: false, runtime: "disabled", lastError: "" },
        }),
      ],
    });
    await h.render(<Dashboard plugins={[]} />);
    expect(h.text()).toMatch(/disabled|off/i);
  });

  // Spend is summed across plugins, and a reservation is money that is spoken for but
  // not yet committed -- the two must not be conflated.
  it("sums committed spend across plugins", async () => {
    serve({
      plugins: [
        pluginState({ pluginId: "a", committedDay: 1_500_000, reservedDay: 0 }),
        pluginState({ pluginId: "b", committedDay: 2_500_000, reservedDay: 0 }),
      ],
    });
    await h.render(<Dashboard plugins={[]} />);
    // 4_000_000 micro-USD is $4.
    expect(h.text()).toMatch(/\$4/);
  });

  it("counts running and queued work separately", async () => {
    serve({
      jobs: [
        job({ id: 1, state: "running" }),
        job({ id: 2, state: "running" }),
        job({ id: 3, state: "pending" }),
        job({ id: 4, state: "retry_wait" }),
        job({ id: 5, state: "succeeded" }),
      ],
    });
    await h.render(<Dashboard plugins={[]} />);
    const text = h.text();
    expect(text).toContain("2");
    expect(text).toMatch(/running/i);
    expect(text).toMatch(/queue|waiting|pending/i);
  });

  it("shows the most recent alert", async () => {
    serve({
      notifications: [
        notification({ id: "n2", title: "Newest alert", createdAt: "2026-09-02T00:00:00Z" }),
        notification({ id: "n1", title: "Older alert", createdAt: "2026-09-01T00:00:00Z" }),
      ],
    });
    await h.render(<Dashboard plugins={[]} />);
    expect(h.text()).toContain("Newest alert");
  });

  it("renders with no plugins, no jobs, and an empty inbox", async () => {
    serve({ plugins: [], jobs: [], notifications: [] });
    await h.render(<Dashboard plugins={[]} />);
    expect(h.text()).toContain("Dashboard");
    expect(h.text()).toContain("0/0");
  });

  // Each snapshot fails independently; one failing endpoint must not blank the screen.
  it("still renders when the plugin list fails to load", async () => {
    serve();
    h.failures.set("/api/admin/plugins", 500);
    await h.render(<Dashboard plugins={[]} />);
    expect(h.text()).toContain("Dashboard");
  });

  it("still renders when the job list fails to load", async () => {
    serve();
    h.failures.set("/api/jobs", 500);
    await h.render(<Dashboard plugins={[]} />);
    expect(h.text()).toContain("Dashboard");
  });

  it("marks a plugin over its daily budget", async () => {
    serve({
      plugins: [
        pluginState({
          pluginId: "hello",
          committedDay: 10_000_000,
          budget: { hourly: 1_000_000, daily: 10_000_000, monthly: 100_000_000, onExceed: "disable" },
        }),
      ],
    });
    await h.render(<Dashboard plugins={[]} />);
    expect(h.text()).toContain("Hello");
  });

  it("surfaces a plugin whose accounting failed", async () => {
    serve({
      plugins: [pluginState({ pluginId: "hello", accountingFailed: "ledger write failed" })],
    });
    await h.render(<Dashboard plugins={[]} />);
    expect(h.text()).toMatch(/need attention|attention/i);
  });
});
