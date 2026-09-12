/**
 * The chime's contract is narrow but easy to get wrong: it must stay silent when
 * muted, it must never throw when the browser refuses audio, and the Test button
 * must ring even when muted.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";

/** A minimal Web Audio stand-in that records what was scheduled. */
function fakeAudio(state: "running" | "suspended" = "running") {
  const started: number[] = [];
  const ctx = {
    state,
    currentTime: 0,
    destination: {},
    resume: () => Promise.resolve(),
    createOscillator: () => ({
      type: "",
      frequency: { value: 0 },
      connect: (n: unknown) => n,
      start: (at: number) => started.push(at),
      stop: () => {},
    }),
    createGain: () => ({
      gain: {
        setValueAtTime: () => {},
        linearRampToValueAtTime: () => {},
        exponentialRampToValueAtTime: () => {},
      },
      connect: (n: unknown) => n,
    }),
  };
  return { ctx, started };
}

async function load() {
  vi.resetModules();
  return import("./chime");
}

describe("chime", () => {
  beforeEach(() => {
    localStorage.clear();
    vi.unstubAllGlobals();
  });

  it("rings both notes when it is not muted", async () => {
    const { ctx, started } = fakeAudio();
    vi.stubGlobal("AudioContext", function () { return ctx; });
    const { ring } = await load();

    ring();
    expect(started).toHaveLength(2);
  });

  it("stays silent when muted, but the test button still rings", async () => {
    const { ctx, started } = fakeAudio();
    vi.stubGlobal("AudioContext", function () { return ctx; });
    const { ring, setMuted, isMuted } = await load();

    setMuted(true);
    expect(isMuted()).toBe(true);
    ring();
    expect(started).toHaveLength(0);

    ring(true);
    expect(started).toHaveLength(2);
  });

  it("says nothing and throws nothing when the browser has no audio", async () => {
    vi.stubGlobal("AudioContext", undefined);
    vi.stubGlobal("webkitAudioContext", undefined);
    const { ring } = await load();

    expect(() => ring(true)).not.toThrow();
  });

  it("does not schedule anything while the context is still suspended", async () => {
    const { ctx, started } = fakeAudio("suspended");
    vi.stubGlobal("AudioContext", function () { return ctx; });
    const { ring } = await load();

    ring(true);
    expect(started).toHaveLength(0);
  });
});
