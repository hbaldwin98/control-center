import { describe, expect, it } from "vitest";
import { liveLabel, liveState } from "./live";

const NOW = 1_700_000_000_000;

describe("liveState", () => {
  it("is off for a plugin that declares no live surface", () => {
    expect(liveState(false, true, NOW, NOW)).toBe("off");
  });

  it("never claims live while the stream is down, however recent the last event", () => {
    expect(liveState(true, false, NOW, NOW)).toBe("stale");
    expect(liveLabel(true, false, NOW)).toBe("the event stream is not connected");
  });

  it("is live for a recent event and idle once it ages out", () => {
    expect(liveState(true, true, NOW - 1_000, NOW)).toBe("live");
    expect(liveState(true, true, NOW - 61_000, NOW)).toBe("idle");
  });

  it("is idle, not live, when a connected stream has delivered nothing yet", () => {
    expect(liveState(true, true, null, NOW)).toBe("idle");
    expect(liveLabel(true, true, null)).toBe("live, nothing yet");
  });
});
