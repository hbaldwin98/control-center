import { createElement } from "react";
import { describe, expect, it } from "vitest";
import type { PluginModule } from "@cc/ui";
import { PluginRegistrationError, reconcile, validateModules } from "./registry";

const hello: PluginModule = {
  id: "hello",
  nav: [{ path: "/hello", label: "Hello" }],
  routes: [{ path: "/hello", element: createElement("div") }],
};

describe("validateModules", () => {
  it("accepts a module whose routes and nav stay under its id", () => {
    expect(validateModules([hello])).toEqual([]);
  });

  it("accepts a plugin nav item promoted to the shell command surface", () => {
    const topLevel: PluginModule = {
      ...hello,
      nav: [{ path: "/hello", label: "Hello", topLevel: true }],
    };
    expect(validateModules([topLevel])).toEqual([]);
  });

  it("accepts a module that omits entry, defaulting to the first nav path", () => {
    expect(validateModules([hello])).toEqual([]);
  });

  it("accepts an entry that names one of the plugin's own nav paths", () => {
    const explicit: PluginModule = {
      ...hello,
      entry: "/hello",
      nav: [
        { path: "/hello/history", label: "History" },
        { path: "/hello", label: "Hello" },
      ],
    };
    expect(validateModules([explicit])).toEqual([]);
  });

  it("rejects an entry outside /<id>", () => {
    const outsider: PluginModule = { ...hello, entry: "/jobs" };
    expect(
      validateModules([outsider]).some((p) => p.includes('entry "/jobs" outside /hello')),
    ).toBe(true);
  });

  it("rejects an entry that is not one of the plugin's nav paths", () => {
    const stray: PluginModule = { ...hello, entry: "/hello/other" };
    expect(
      validateModules([stray]).some((p) =>
        p.includes('entry "/hello/other" that is not one of its nav paths'),
      ),
    ).toBe(true);
  });

  it("rejects a path outside /<id>", () => {
    const outsider: PluginModule = {
      id: "hello",
      nav: [{ path: "/jobs", label: "Nope" }],
      routes: [{ path: "/jobs", element: createElement("div") }],
    };
    const problems = validateModules([outsider]);
    expect(problems.some((p) => p.includes('route "/jobs" outside /hello'))).toBe(true);
    expect(problems.some((p) => p.includes('nav "/jobs" outside /hello'))).toBe(true);
  });

  it("rejects a duplicate frontend id and a colliding route", () => {
    const twin: PluginModule = {
      id: "hello",
      nav: [],
      routes: [{ path: "/hello", element: createElement("div") }],
    };
    expect(validateModules([hello, twin]).some((p) => p.includes("duplicate frontend plugin id"))).toBe(
      true,
    );

    const other: PluginModule = {
      id: "other",
      nav: [],
      routes: [{ path: "/hello", element: createElement("div") }],
    };
    const problems = validateModules([hello, other]);
    expect(problems.some((p) => p.includes("claimed by both"))).toBe(true);
  });

  it("rejects an invalid plugin id", () => {
    const bad: PluginModule = { id: "Hello", nav: [], routes: [] };
    expect(validateModules([bad]).some((p) => p.includes("must match"))).toBe(true);
  });

  it("accepts a dashboard contribution with valid live patterns", () => {
    const live: PluginModule = {
      ...hello,
      dashboard: { summary: "Ticks.", live: ["hello.ticked", "hello.**", "core.ai.usage"] },
    };
    expect(validateModules([live])).toEqual([]);
  });

  it("rejects a live pattern that can never match, so the indicator cannot lie", () => {
    const broken: PluginModule = {
      ...hello,
      dashboard: { live: ["Hello.Ticked"] },
    };
    expect(
      validateModules([broken]).some((p) => p.includes('invalid live pattern "Hello.Ticked"')),
    ).toBe(true);
  });

  it("leaves a plugin that contributes no dashboard alone", () => {
    expect(validateModules([{ ...hello, dashboard: {} }])).toEqual([]);
  });
});

describe("reconcile", () => {
  it("returns the modules when frontend and backend ids agree", () => {
    expect(reconcile([hello], [{ id: "hello", name: "Hello", enabled: true }])).toEqual([
      hello,
    ]);
  });

  it("fails closed on a frontend module the backend does not know", () => {
    expect(() => reconcile([hello], [])).toThrow(PluginRegistrationError);
  });

  it("fails closed on a backend plugin with no frontend module", () => {
    expect(() =>
      reconcile([], [{ id: "hello", name: "Hello", enabled: true }]),
    ).toThrow(/has no frontend module/);
  });

  it("fails closed when the backend reports the same id twice", () => {
    const d = { id: "hello", name: "Hello", enabled: true };
    expect(() => reconcile([hello], [d, d])).toThrow(/more than once/);
  });
});
