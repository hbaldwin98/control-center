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
