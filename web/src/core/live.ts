/**
 * How a tile decides what to claim about liveness.
 *
 * Two independent facts have to agree before anything says "live": the plugin has to have
 * declared live surfaces, and the one shared stream has to actually be connected. A dot
 * that pulses while the connection is down is worse than no dot at all, so the connection
 * state wins.
 */
export type LiveState = "live" | "idle" | "stale" | "off";

/** Anything newer than this counts as "right now" for the indicator. */
export const FRESH_MS = 60_000;

export function liveState(
  declared: boolean,
  connected: boolean,
  lastAt: number | null,
  now = Date.now(),
): LiveState {
  if (!declared) return "off";
  if (!connected) return "stale";
  if (lastAt !== null && now - lastAt < FRESH_MS) return "live";
  return "idle";
}

export function liveLabel(declared: boolean, connected: boolean, lastAt: number | null): string {
  if (!declared) return "not a live view";
  if (!connected) return "the event stream is not connected";
  if (lastAt === null) return "live, nothing yet";
  return "live";
}
