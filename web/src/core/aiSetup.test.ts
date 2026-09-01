import { describe, expect, it } from "vitest";
import { modelsReady } from "./aiSetup";
import type { ModelNeed } from "./types";

function need(status: ModelNeed["status"]): ModelNeed {
  return { name: "cheap-chat", capabilities: ["chat"], purpose: "x", status, healthy: status === "ready" };
}

describe("modelsReady", () => {
  it("is true when a plugin declares no AI", () => {
    expect(modelsReady(undefined)).toBe(true);
    expect(modelsReady([])).toBe(true);
  });

  it("is true only when every declared route is ready", () => {
    expect(modelsReady([need("ready"), need("ready")])).toBe(true);
    expect(modelsReady([need("ready"), need("missing")])).toBe(false);
  });
});
