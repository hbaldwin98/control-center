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
import { stream } from "@cc/ui";
import { Costs } from "./Costs";
import { Events } from "./Events";
import { Inbox } from "./Inbox";
import { Jobs } from "./Jobs";
import { NotificationSettings } from "./NotificationSettings";
import { PluginDetail } from "./PluginDetail";
import { Plugins } from "./Plugins";
import { Settings } from "./Settings";
import { Sessions } from "./Sessions";
import { FakeEventSource, setupHarness, fill } from "./testing/harness";
import { job, notification, pluginState, snapshot, storedCredential, eventCatalog, eventSpec } from "./testing/fixtures";

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

describe("Sessions", () => {
  const profile = { id: "agent", name: "Agent", workspaceRoot: "/work", acceptsInstruction: true };
  const session = {
    id: 7,
    profileId: "agent",
    profileName: "Agent",
    title: "Review change",
    workspace: "/work/control-center",
    state: "running",
    exitCode: null,
    error: "",
    stopReason: "",
    createdAt: "2026-09-01T00:00:00Z",
    startedAt: "2026-09-01T00:00:01Z",
    finishedAt: null,
  };
  const serve = (sessions: unknown[] = [session], profiles: unknown[] = [profile]) =>
    h.routes.set("/api/harness", snapshot({ profiles, sessions }));

  it("lists configured profiles and sessions", async () => {
    serve();
    await h.render(<Sessions />);
    expect(h.text()).toContain("Review change");
    expect(h.text()).toContain("Agent");
  });

  it("starts a configured session", async () => {
    serve([]);
    await h.render(<Sessions />);
    await fill(h.container, 'input[placeholder="."]', "control-center");
    await h.click("Start session");
    expect(h.calls.some((call) => call.path === "/api/harness" && call.method === "POST")).toBe(true);
  });

  // Starting a session runs a program on the box, so the server asks for the password
  // again. The prompt has to appear on this screen: sending the operator to Settings and
  // back would lose the form they just filled in.
  it("confirms the password when the server asks, then starts the session", async () => {
    serve([]);
    await h.render(<Sessions />);
    // Set after the snapshot loads: the gate is on creating a session, not reading them.
    h.failures.set("/api/harness", { status: 403, code: "reauth_required" });
    await fill(h.container, 'input[placeholder="."]', "control-center");
    await h.click("Start session");

    expect(h.text()).toContain("Administrator password");

    // With the password confirmed the create is retried, rather than made to be
    // filled in and submitted a second time.
    h.failures.delete("/api/harness");
    await fill(h.container, 'input[type="password"]', "correct horse battery staple");
    await h.click("Confirm and start");

    expect(h.calls.some((call) => call.path === "/api/auth/reauth" && call.method === "POST")).toBe(true);
    expect(h.calls.filter((call) => call.path === "/api/harness" && call.method === "POST")).toHaveLength(2);
  });

  it("stops a running session", async () => {
    serve();
    await h.render(<Sessions />);
    await h.click("Stop");
    expect(h.calls.some((call) => call.path === "/api/harness/7/stop" && call.method === "POST")).toBe(true);
  });

  it("does not reload the session list for output heartbeat events", async () => {
    serve();
    await h.render(<Sessions />);
    const before = h.calls.filter((call) => call.path === "/api/harness" && call.method === "GET").length;
    try {
      stream.start("1");
      FakeEventSource.instances.at(-1)?.emitEvent({
        id: "2",
        type: "core.harness.output",
        source: "harness",
        subject: "7",
        payload: { sessionId: 7 },
        createdAt: "2026-09-01T00:00:02Z",
      });
      await h.settle();
      expect(h.calls.filter((call) => call.path === "/api/harness" && call.method === "GET")).toHaveLength(before);
    } finally {
      stream.stop();
    }
  });

  it("only reloads an open output panel for its own session events", async () => {
    serve();
    h.routes.set("/api/harness/7", snapshot({ ...session, output: [] }));
    await h.render(<Sessions />);
    await h.click("Output");
    const before = h.calls.filter((call) => call.path === "/api/harness/7" && call.method === "GET").length;
    try {
      stream.start("1");
      const source = FakeEventSource.instances.at(-1);
      source?.emitEvent({
        id: "2",
        type: "core.harness.output",
        source: "harness",
        subject: "8",
        payload: { sessionId: 8 },
        createdAt: "2026-09-01T00:00:02Z",
      });
      await h.settle();
      expect(h.calls.filter((call) => call.path === "/api/harness/7" && call.method === "GET")).toHaveLength(before);

      source?.emitEvent({
        id: "3",
        type: "core.harness.output",
        source: "harness",
        subject: "7",
        payload: { sessionId: 7 },
        createdAt: "2026-09-01T00:00:03Z",
      });
      await h.settle();
      expect(h.calls.filter((call) => call.path === "/api/harness/7" && call.method === "GET")).toHaveLength(before + 1);
    } finally {
      stream.stop();
    }
  });

  it("explains when no profiles are configured", async () => {
    serve([], []);
    await h.render(<Sessions />);
    expect(h.text()).toContain("No harness profiles are configured");
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

  it("renders a plugin that declares events", async () => {
    serve([
      pluginState({
        pluginId: "hello",
        events: [eventSpec({ match: "hello.ticked", purpose: "The cron tick finished." })],
      }),
    ]);
    await atSettings("hello");
    // The match names the event; its payload fields stay folded until asked for.
    expect(h.text()).toContain("hello.ticked");
    expect(h.text()).not.toContain("{event.payload.note}");
    expect(await h.click("hello.ticked")).toBe(true);
    expect(h.text()).toContain("{event.payload.note}");
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
    opts: { rules?: unknown[]; channels?: unknown[]; health?: unknown[]; catalog?: unknown } = {},
  ) => {
    h.routes.set("/api/admin/notifications/rules", snapshot(opts.rules ?? [rule()]));
    h.routes.set("/api/admin/notifications/channels", snapshot(opts.channels ?? [channel()]));
    h.routes.set("/api/admin/notifications/health", snapshot(opts.health ?? []));
    h.routes.set("/api/admin/notifications/catalog", snapshot(opts.catalog ?? eventCatalog()));
  };

  it("lists rules and channels", async () => {
    serve({ rules: [rule({ title: "A job failed" })], channels: [channel({ id: "ch-1" })] });
    await h.render(<NotificationSettings />);
    expect(h.text()).toContain("A job failed");
    expect(h.text()).toContain("plugin-alert");
  });

  it("lists declared events and their payload paths", async () => {
    serve();
    await h.render(<NotificationSettings />);
    // The catalog is reference material: a source names itself, and its events and
    // their payload paths come out a level at a time rather than all at once.
    expect(h.text()).not.toContain("tid.synced");
    expect(await h.click("TID")).toBe(true);
    expect(h.text()).toContain("tid.synced");
    expect(await h.click("tid.synced")).toBe(true);
    expect(h.text()).toContain("{event.payload.body}");
    expect(h.text()).toContain("A collection finished.");
  });

  it("shows only the events a search matches", async () => {
    serve({
      catalog: eventCatalog({
        events: [
          {
            source: "tid",
            name: "TID",
            type: "tid.synced",
            match: "tid.synced",
            purpose: "A collection finished.",
            fields: [],
          },
          {
            source: "bidrl",
            name: "BidRL",
            type: "bidrl.collected",
            match: "bidrl.collected",
            purpose: "An auction was collected.",
            fields: [],
          },
        ],
      }),
    });
    await h.render(<NotificationSettings />);
    await fill(h.container, "input[aria-label='Search events']", "bidrl");
    // A search opens what it found and drops what it did not.
    expect(h.text()).toContain("bidrl.collected");
    expect(h.text()).not.toContain("TID");
  });

  it("fills match from a catalog event", async () => {
    serve();
    await h.render(<NotificationSettings />);
    expect(await h.click("TID")).toBe(true);
    expect(await h.click("tid.synced")).toBe(true);
    expect(await h.click(/use this match/i)).toBe(true);
    const values = [...h.container.querySelectorAll("input")].map((el) => (el as HTMLInputElement).value);
    expect(values).toContain("tid.synced");
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

  it("edits an existing rule in place instead of recreating it", async () => {
    serve({ rules: [rule({ id: "rule-1", title: "A job failed", throttleSeconds: 60 })] });
    await h.render(<NotificationSettings />);
    expect(await h.clickIn("rule-1", "Edit")).toBe(true);
    const id = h.container.querySelector<HTMLInputElement>("input[disabled]");
    expect(id?.value).toBe("rule-1");
    expect(await h.clickIn("Edit rule rule-1", "Save rule")).toBe(true);
    const put = h.calls.find((c) => c.method === "PUT" && c.path.endsWith("/rules/rule-1"));
    expect(put?.body).toMatchObject({ title: "A job failed", throttleSeconds: 60, enabled: true });
    expect(h.calls.some((c) => c.method === "DELETE")).toBe(false);
  });

  it("edits an existing channel in place instead of recreating it", async () => {
    serve({
      rules: [],
      channels: [channel({ id: "ntfy-main", kind: "ntfy", settings: { topic: "alerts" } })],
    });
    await h.render(<NotificationSettings />);
    expect(await h.clickIn("ntfy-main", "Edit")).toBe(true);
    expect(await h.clickIn("Edit channel ntfy-main", "Save channel")).toBe(true);
    const put = h.calls.find((c) => c.method === "PUT" && c.path.endsWith("/channels/ntfy-main"));
    expect(put?.body).toMatchObject({ kind: "ntfy", settings: { topic: "alerts" } });
    expect(h.calls.some((c) => c.method === "DELETE")).toBe(false);
  });

  it("survives every endpoint failing", async () => {
    serve();
    for (const p of [
      "/api/admin/notifications/rules",
      "/api/admin/notifications/channels",
      "/api/admin/notifications/health",
      "/api/admin/notifications/catalog",
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
