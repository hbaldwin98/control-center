/**
 * Fixtures for the core screen tests.
 *
 * Each builder returns every field the Go handler emits, so a screen under test never
 * sees a shape the server could not send. Overrides are shallow and per-field, which is
 * enough for the branches these tests reach.
 */
import type { Budget, EventCatalog, EventSpec, Job, ModelNeed, PluginState } from "../types";

/**
 * The only runtime values the host emits (`internal/core/pluginhost/pluginhost.go`).
 * Anything else is a shape the server cannot send, so a test asserting on one proves
 * nothing about production.
 */
export type Runtime = "enabled" | "disabled" | "degraded";

export const budget: Budget = {
  hourly: 1_000_000,
  daily: 10_000_000,
  monthly: 100_000_000,
  onExceed: "reject",
};

export function pluginState(over: Partial<PluginState> = {}): PluginState {
  return {
    pluginId: "hello",
    enabled: true,
    automated: false,
    disabledAt: null,
    disabledBy: "",
    disabledReason: "",
    budget,
    reservedHour: 0,
    committedHour: 250_000,
    reservedDay: 100_000,
    committedDay: 2_500_000,
    reservedMonth: 100_000,
    committedMonth: 20_000_000,
    accountingFailed: null,
    name: "Hello",
    description: "A sample plugin",
    health: { desiredEnabled: true, runtime: "enabled", lastError: "" },
    models: [],
    events: [],
    ...over,
  };
}

export function modelNeed(over: Partial<ModelNeed> = {}): ModelNeed {
  return {
    name: "cheap-chat",
    capabilities: ["chat", "json"],
    purpose: "Classify a lot from its title.",
    status: "ready",
    healthy: true,
    provider: "openai",
    model: "gpt-4o-mini",
    ...over,
  };
}

export function eventSpec(over: Partial<EventSpec> = {}): EventSpec {
  return {
    type: "hello.ticked",
    match: "hello.ticked",
    purpose: "The cron tick finished.",
    fields: [
      { name: "note", type: "string", purpose: "Configured note.", path: "event.payload.note" },
    ],
    ...over,
  };
}

export function eventCatalog(over: Partial<EventCatalog> = {}): EventCatalog {
  return {
    envelope: [
      { path: "event.type", type: "string", purpose: "Fully qualified type." },
      { path: "event.subject", type: "string", purpose: "Stable entity id or a short headline." },
    ],
    events: [
      {
        source: "tid",
        name: "TID",
        type: "tid.synced",
        match: "tid.synced",
        purpose: "A collection finished.",
        fields: [
          { name: "body", type: "string", purpose: "One-line reading.", path: "event.payload.body" },
          { name: "day", type: "string", purpose: "Latest calendar day.", path: "event.payload.day" },
        ],
      },
    ],
    ...over,
  };
}

export function job(over: Partial<Job> = {}): Job {
  return {
    id: 1,
    pluginId: "hello",
    name: "tick",
    state: "succeeded",
    attempt: 1,
    maxAttempts: 3,
    progress: 1,
    progressMessage: "done",
    createdAt: "2026-09-01T00:00:00Z",
    startedAt: "2026-09-01T00:00:01Z",
    finishedAt: "2026-09-01T00:00:02Z",
    ...over,
  };
}

/**
 * Providers, routes, and credentials as `core/Models.tsx` declares them. Those types are
 * local to that module, so these builders restate the shape rather than importing it --
 * a mismatch shows up as a type error there, which is the point.
 */
export function provider(over: Record<string, unknown> = {}) {
  return {
    id: "openai",
    kind: "openai_compatible",
    baseUrl: "https://api.openai.com/v1",
    credentialId: "c1",
    billing: "metered",
    ...over,
  };
}

export function attempt(over: Record<string, unknown> = {}) {
  return {
    provider: "openai",
    model: "gpt-4o-mini",
    billing: "metered",
    inputMicroUsdPerMillion: 150_000,
    outputMicroUsdPerMillion: 600_000,
    ...over,
  };
}

export function route(over: Record<string, unknown> = {}) {
  return {
    logicalName: "cheap-chat",
    capabilities: ["chat", "json"],
    maxInputTokens: 8_000,
    maxOutputTokens: 1_000,
    attemptPlan: [attempt()],
    healthy: true,
    lastError: "",
    ...over,
  };
}

/** The credential shape `core/Models.tsx` reads: just enough to pick one. */
export function credential(over: Record<string, unknown> = {}) {
  return { id: "c1", kind: "api_key", provider: "openai", ...over };
}

/**
 * The credential shape `core/Settings.tsx` reads, which is the fuller one -- that screen
 * manages credentials rather than merely choosing between them, so it needs the status,
 * version, and expiry the manage endpoints return.
 */
export function storedCredential(over: Record<string, unknown> = {}) {
  return {
    id: "openai-key",
    kind: "api_key",
    provider: "openai",
    status: "ok",
    version: 1,
    expiresAt: null,
    scopes: [],
    ...over,
  };
}

export function notification(over: Record<string, unknown> = {}) {
  return {
    id: "n1",
    eventId: "12",
    pluginId: "hello",
    title: "Something happened",
    body: "A rule matched an event.",
    subject: "hello",
    read: false,
    createdAt: "2026-09-01T00:00:00Z",
    availableAt: "2026-09-01T00:00:00Z",
    ruleId: "rule-1",
    ...over,
  };
}

/** Wraps a payload in the snapshot envelope every core endpoint returns. */
export function snapshot<T>(data: T, asOfEventId = "1") {
  return { data, asOfEventId };
}
