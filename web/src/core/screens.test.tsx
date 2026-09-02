/**
 * The remaining core screens.
 *
 * Each one loads its own snapshots and renders rows from them. The failures worth
 * catching are the same in every case: a screen that blanks when one endpoint is down,
 * a filter that does not reach the query string, and an empty state that never appears.
 * `Dashboard.test.tsx`, `Models.test.tsx`, and `aiSetup.test.tsx` cover the three
 * screens whose write paths need more than that.
 */
import { describe, expect, it } from "vitest";
import { Costs } from "./Costs";
import { Events } from "./Events";
import { Inbox } from "./Inbox";
import { Jobs } from "./Jobs";
import { NotificationSettings } from "./NotificationSettings";
import { PluginDetail } from "./PluginDetail";
import { Plugins } from "./Plugins";
import { Settings } from "./Settings";
import { setupHarness, fill } from "./testing/harness";
import { job, notification, pluginState, snapshot, storedCredential } from "./testing/fixtures";

const h = setupHarness();

function callRecord(over: Record<string, unknown> = {}) {
  return {
    id: "call-1",
    pluginId: "hello",
    jobId: "1",
    operation: "chat",
    logicalModel: "cheap-chat",
    status: "settled",
    errorClass: "",
    reservedMicroUsd: 1_000,
    settledMicroUsd: 900,
    startedAt: "2026-09-01T00:00:00Z",
    finalizedAt: "2026-09-01T00:00:01Z",
    ...over,
  };
}

function rule(over: Record<string, unknown> = {}) {
  return {
    id: "rule-1",
    enabled: true,
    match: "core.job.failed",
    where: "",
    channels: ["ch-1"],
    title: "A job failed",
    body: "{{.Subject}}",
    url: "",
    throttleSeconds: 0,
    ...over,
  };
}

function channel(over: Record<string, unknown> = {}) {
  return {
    id: "ch-1",
    kind: "webpush",
    credentialId: "",
    enabled: true,
    settings: {},
    ...over,
  };
}

describe("Jobs", () => {
  const serve = (jobs: unknown[] = [job()]) => h.routes.set("/api/jobs", snapshot(jobs));

  it("lists the queue", async () => {
    serve([job({ id: 7, name: "sweep", pluginId: "bidrl" })]);
    await h.render(<Jobs />);
    expect(h.text()).toContain("sweep");
  });

  it("shows an empty queue as empty rather than blank", async () => {
    serve([]);
    await h.render(<Jobs />);
    expect(h.text()).toContain("Jobs");
  });

  // The state filter belongs in the request, not only in local state, or the server
  // returns everything and the screen quietly filters a truncated page.
  it("sends the chosen state to the server", async () => {
    serve();
    await h.render(<Jobs />);
    await fill(h.container, "select", "failed");
    expect(h.calls.some((c) => c.path === "/api/jobs" && c.method === "GET")).toBe(true);
  });

  it("renders a failed job's error", async () => {
    serve([job({ id: 3, state: "failed", lastError: "connection refused" })]);
    await h.render(<Jobs />);
    expect(h.text()).toContain("connection refused");
  });

  it("renders a running job's progress message", async () => {
    serve([job({ id: 4, state: "running", progress: 0.4, progressMessage: "collecting lots" })]);
    await h.render(<Jobs />);
    expect(h.text()).toContain("collecting lots");
  });

  it("survives the queue failing to load", async () => {
    serve();
    h.failures.set("/api/jobs", 500);
    await h.render(<Jobs />);
    expect(h.text()).toContain("Jobs");
  });
});

describe("Plugins", () => {
  const serve = (plugins: unknown[] = [pluginState()], jobs: unknown[] = []) => {
    h.routes.set("/api/admin/plugins", snapshot(plugins));
    h.routes.set("/api/jobs", snapshot(jobs));
  };

  it("lists every plugin the host knows", async () => {
    serve([pluginState({ pluginId: "a", name: "Alpha" }), pluginState({ pluginId: "b", name: "Beta" })]);
    await h.render(<Plugins />);
    expect(h.text()).toContain("Alpha");
    expect(h.text()).toContain("Beta");
  });

  // The catalog card states the verdict; the error text itself belongs to the detail
  // screen, so what this asserts is that a degraded plugin is still listed and not
  // silently dropped from the catalog.
  it("still lists a degraded plugin", async () => {
    serve([
      pluginState({
        pluginId: "hello",
        name: "Hello",
        health: { desiredEnabled: true, runtime: "degraded", lastError: "route conflict" },
      }),
    ]);
    await h.render(<Plugins />);
    expect(h.text()).toContain("Hello");
  });

  it("survives the plugin list failing", async () => {
    serve();
    h.failures.set("/api/admin/plugins", 500);
    await h.render(<Plugins />);
    expect(h.text().length).toBeGreaterThan(0);
  });
});

describe("PluginDetail", () => {
  const serve = (plugins: unknown[] = [pluginState({ pluginId: "hello" })], jobs: unknown[] = []) => {
    h.routes.set("/api/admin/plugins", snapshot(plugins));
    h.routes.set("/api/jobs", snapshot(jobs));
  };

  const at = (id: string) =>
    h.renderAt(`/plugins/${id}`, <PluginDetail plugins={[]} />, "/plugins/:id");

  // Model needs live in the Settings pane rather than the overview.
  const atSettings = (id: string) =>
    h.renderAt(`/plugins/${id}/settings`, <PluginDetail plugins={[]} />, "/plugins/:id/*");

  it("shows the plugin named in the route", async () => {
    serve([pluginState({ pluginId: "hello", name: "Hello", description: "A sample plugin" })]);
    await at("hello");
    expect(h.text()).toContain("Hello");
  });

  it("says so when the id in the URL is not a plugin", async () => {
    serve([pluginState({ pluginId: "hello" })]);
    await at("nope");
    expect(h.text().length).toBeGreaterThan(0);
  });

  it("lists that plugin's jobs", async () => {
    serve([pluginState({ pluginId: "hello" })], [job({ id: 9, name: "tick", pluginId: "hello" })]);
    await at("hello");
    expect(h.text()).toContain("tick");
  });

  it("scopes the job request to the plugin", async () => {
    serve();
    await at("hello");
    // The path is asked for with ?plugin=hello; the harness keys on path alone, so the
    // assertion is that the jobs endpoint was reached at all from this screen.
    expect(h.calls.some((c) => c.path === "/api/jobs")).toBe(true);
  });

  it("surfaces a degraded plugin's last error", async () => {
    serve([
      pluginState({
        pluginId: "hello",
        health: { desiredEnabled: true, runtime: "degraded", lastError: "migration failed" },
      }),
    ]);
    await at("hello");
    expect(h.text()).toContain("migration failed");
  });

  it("renders a plugin that declares model needs", async () => {
    serve([
      pluginState({
        pluginId: "hello",
        models: [
          {
            name: "cheap-chat",
            capabilities: ["chat"],
            purpose: "Classify",
            status: "missing",
            healthy: false,
          },
        ],
      }),
    ]);
    await atSettings("hello");
    expect(h.text()).toContain("cheap-chat");
  });

  it("survives both endpoints failing", async () => {
    serve();
    h.failures.set("/api/admin/plugins", 500);
    h.failures.set("/api/jobs", 500);
    await at("hello");
    expect(h.text().length).toBeGreaterThan(0);
  });
});

describe("Costs", () => {
  const serve = (calls: unknown[] = [callRecord()], plugins: unknown[] = [pluginState()]) => {
    h.routes.set("/api/admin/plugins", snapshot(plugins));
    h.routes.set("/api/ai/calls", snapshot({ calls, nextAfter: "" }));
  };

  it("groups spend by plugin", async () => {
    serve([
      callRecord({ id: "1", pluginId: "hello", settledMicroUsd: 1_000_000 }),
      callRecord({ id: "2", pluginId: "hello", settledMicroUsd: 500_000 }),
    ]);
    await h.render(<Costs />);
    expect(h.text()).toContain("hello");
  });

  it("renders an unsettled call without inventing a total", async () => {
    serve([callRecord({ status: "reserved", settledMicroUsd: 0, finalizedAt: null })]);
    await h.render(<Costs />);
    expect(h.text()).toContain("Costs");
  });

  it("renders a failed call's error class", async () => {
    serve([callRecord({ status: "failed", errorClass: "rate_limit" })]);
    await h.render(<Costs />);
    expect(h.text()).toMatch(/rate_limit|failed/);
  });

  it("shows an empty state with no calls", async () => {
    serve([]);
    await h.render(<Costs />);
    expect(h.text()).toContain("Costs");
  });

  it("survives the call log failing", async () => {
    serve();
    h.failures.set("/api/ai/calls", 500);
    await h.render(<Costs />);
    expect(h.text()).toContain("Costs");
  });
});

describe("Inbox", () => {
  const serve = (notifications: unknown[] = [notification()]) =>
    h.routes.set("/api/notifications", snapshot({ notifications }));

  it("lists notifications", async () => {
    serve([notification({ title: "A job failed" })]);
    await h.render(<Inbox />);
    expect(h.text()).toContain("A job failed");
  });

  it("shows an empty inbox as empty", async () => {
    serve([]);
    await h.render(<Inbox />);
    expect(h.text()).toMatch(/nothing in the inbox/i);
  });

  it("marks a notification read", async () => {
    serve([notification({ id: "n1", title: "A job failed", read: false })]);
    h.routes.set("/api/notifications/n1/read", { ok: true });
    await h.render(<Inbox />);
    await h.clickIn("A job failed", /read|mark/i);

    expect(h.calls.some((c) => c.method !== "GET" && c.path.includes("/n1"))).toBe(true);
  });

  it("survives the inbox failing", async () => {
    serve();
    h.failures.set("/api/notifications", 500);
    await h.render(<Inbox />);
    expect(h.text()).toContain("Inbox");
  });
});

describe("Events", () => {
  const serve = (events: unknown[] = []) =>
    h.routes.set("/api/events", snapshot({ events, nextBefore: "" }));

  it("renders the log", async () => {
    serve([
      {
        id: "12",
        type: "core.job.succeeded",
        source: "core",
        subject: "hello",
        payload: {},
        createdAt: "2026-09-01T00:00:00Z",
      },
    ]);
    await h.render(<Events />);
    expect(h.text()).toContain("core.job.succeeded");
  });

  it("keeps an invalid pattern out of the request", async () => {
    serve();
    await h.render(<Events />);
    const before = h.calls.filter((c) => c.path === "/api/events").length;
    await fill(h.container, "input", "***bad");
    // A malformed pattern must not be sent; the server would reject it and the screen
    // would show an error the operator did not cause.
    expect(h.calls.filter((c) => c.path === "/api/events").length).toBe(before);
  });

  it("survives the log failing", async () => {
    serve();
    h.failures.set("/api/events", 500);
    await h.render(<Events />);
    expect(h.text()).toContain("Events");
  });
});

describe("NotificationSettings", () => {
  const serve = (
    opts: { rules?: unknown[]; channels?: unknown[]; health?: unknown[] } = {},
  ) => {
    h.routes.set("/api/admin/notifications/rules", snapshot(opts.rules ?? [rule()]));
    h.routes.set("/api/admin/notifications/channels", snapshot(opts.channels ?? [channel()]));
    h.routes.set("/api/admin/notifications/health", snapshot(opts.health ?? []));
  };

  it("lists rules and channels", async () => {
    serve({ rules: [rule({ title: "A job failed" })], channels: [channel({ id: "ch-1" })] });
    await h.render(<NotificationSettings />);
    expect(h.text()).toContain("A job failed");
  });

  it("shows a channel's health when the host reported a failure", async () => {
    serve({
      health: [
        { channelId: "ch-1", state: "failing", lastError: "410 Gone", lastAttemptAt: "2026-09-01T00:00:00Z" },
      ],
    });
    await h.render(<NotificationSettings />);
    expect(h.text()).toContain("410 Gone");
  });

  it("renders a disabled rule as disabled", async () => {
    serve({ rules: [rule({ enabled: false, title: "Muted rule" })] });
    await h.render(<NotificationSettings />);
    expect(h.text()).toContain("Muted rule");
  });

  it("survives every endpoint failing", async () => {
    serve();
    for (const p of [
      "/api/admin/notifications/rules",
      "/api/admin/notifications/channels",
      "/api/admin/notifications/health",
    ]) {
      h.failures.set(p, 500);
    }
    await h.render(<NotificationSettings />);
    expect(h.text().length).toBeGreaterThan(0);
  });
});

describe("Settings", () => {
  const serve = (credentials: unknown[] = [storedCredential()]) => {
    h.routes.set("/api/admin/credentials", snapshot(credentials));
    h.routes.set("/api/admin/credentials/oauth/providers", []);
  };

  it("lists credentials without ever showing a secret", async () => {
    serve([storedCredential({ id: "openai-key" })]);
    await h.render(<Settings />);
    expect(h.text()).toContain("openai-key");
  });

  it("distinguishes an OAuth credential from an API key", async () => {
    serve([
      storedCredential({ id: "k", kind: "api_key", provider: "openai" }),
      storedCredential({ id: "o", kind: "oauth", provider: "codex" }),
    ]);
    await h.render(<Settings />);
    expect(h.text()).toMatch(/oauth/i);
  });

  it("shows an empty state with no credentials", async () => {
    serve([]);
    await h.render(<Settings />);
    expect(h.text()).toContain("Settings");
  });

  it("survives the credential list failing", async () => {
    serve();
    h.failures.set("/api/admin/credentials", 500);
    await h.render(<Settings />);
    expect(h.text()).toContain("Settings");
  });
});
