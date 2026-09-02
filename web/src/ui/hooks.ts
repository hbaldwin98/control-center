/**
 * Live-data hooks. A plugin gets live data from these without knowing how the transport
 * works.
 */
import { useCallback, useEffect, useRef, useState } from "react";
import { idAbove, stream } from "./stream";
import type { StreamStatus } from "./stream";
import type { Event, Snapshot } from "./types";

export type UseEventsOptions<T> = {
  /** Narrow beyond the pattern, for example to one plugin's usage rows. */
  filter?: (event: Event<T>) => boolean;
  /** How many events to retain. Defaults to 200. */
  limit?: number;
};

/**
 * Subscribes to the shared stream and returns the matching events, newest last.
 *
 * ```tsx
 * const deals = useEvents("bidrl.deal_found");
 * const cost  = useEvents<AIUsage>("core.ai.usage", {
 *   filter: e => e.payload.plugin === "bidrl",
 * });
 * ```
 */
export function useEvents<T = unknown>(
  pattern: string,
  options: UseEventsOptions<T> = {},
): Event<T>[] {
  const { filter, limit = 200 } = options;
  const [events, setEvents] = useState<Event<T>[]>([]);

  // Keep the latest filter without resubscribing on every render.
  const filterRef = useRef(filter);
  filterRef.current = filter;

  useEffect(() => {
    setEvents([]);
    return stream.subscribe(pattern, (event) => {
      const typed = event as Event<T>;
      if (filterRef.current && !filterRef.current(typed)) return;
      setEvents((prev) => {
        const next = [...prev, typed];
        return next.length > limit ? next.slice(next.length - limit) : next;
      });
    });
  }, [pattern, limit]);

  return events;
}

/** Called for each event that arrives after a snapshot's boundary. */
/**
 * Folds one later event into the current state. Returning `undefined` means this event
 * cannot be folded, and the snapshot is refetched instead -- which is what lets a
 * reducer handle the one event it understands without having to model every other event
 * its pattern matches.
 */
export type ApplyEvent<T> = (current: T, event: Event) => T | undefined;

export type UseSnapshotOptions<T> = {
  /** Stream pattern, or patterns, whose events update this snapshot. */
  events?: string | readonly string[];
  /** Folds one later event into the current state. Omit to refetch instead. */
  apply?: ApplyEvent<T>;
};

export type SnapshotState<T> =
  | { status: "loading"; data: null; error: null }
  | { status: "ready"; data: T; error: null }
  | { status: "error"; data: null; error: Error };

export type UseSnapshotResult<T> = SnapshotState<T> & {
  /** Reloads through the registered loader. The shell also calls this after a reset. */
  reload: () => void;
};

function patternList(events: string | readonly string[] | undefined): string[] {
  if (!events) return [];
  return typeof events === "string" ? [events] : [...events];
}

/**
 * Loads a state snapshot and keeps it live.
 *
 * It starts a pattern-filtered buffer on the already-open shared stream *before* sending
 * the request, installs the snapshot, then applies buffered events strictly above
 * `asOfEventId`. Without `apply`, a matching event invalidates the snapshot and it
 * refetches — which is the right shape for job progress, logs, and notifications.
 *
 * `loader` must be referentially stable (`useCallback`). When its identity changes — a
 * new filter query, a different resource id — the snapshot reloads from that loader.
 */
export function useSnapshot<T>(
  loader: (signal: AbortSignal) => Promise<Snapshot<T>>,
  options: UseSnapshotOptions<T> = {},
): UseSnapshotResult<T> {
  const { events, apply } = options;
  const patternKey = patternList(events).join("\n");
  const [state, setState] = useState<SnapshotState<T>>({
    status: "loading",
    data: null,
    error: null,
  });
  const [generation, setGeneration] = useState(0);
  const epoch = useStreamEpoch();

  const loaderRef = useRef(loader);
  loaderRef.current = loader;
  const applyRef = useRef(apply);
  applyRef.current = apply;

  const reload = useCallback(() => setGeneration((n) => n + 1), []);

  useEffect(() => {
    const controller = new AbortController();
    let cancelled = false;
    const patterns = patternKey === "" ? [] : patternKey.split("\n");

    // Open buffers first: anything committed between the server's read and the
    // install below is held here rather than lost.
    const buffers = patterns.map((p) => stream.openBuffer(p));
    const unsubs: (() => void)[] = [];

    (async () => {
      try {
        const snap = await loaderRef.current(controller.signal);
        if (cancelled) return;

        let current = snap.data;

        // Apply only what the snapshot does not already reflect, in stream order.
        const pending = buffers
          .flatMap((b) => b.drain())
          .sort((a, b) => (idAbove(a.id, b.id) ? 1 : idAbove(b.id, a.id) ? -1 : 0));
        const seen = new Set<string>();
        for (const event of pending) {
          if (!idAbove(event.id, snap.asOfEventId) || seen.has(event.id)) continue;
          seen.add(event.id);
          const folded = applyRef.current?.(current, event);
          if (folded === undefined) {
            // No reducer, or one that cannot fold this event: it is an invalidation.
            // Refetch instead of guessing.
            for (const b of buffers) b.close();
            if (!cancelled) reload();
            return;
          }
          current = folded;
        }
        setState({ status: "ready", data: current, error: null });

        if (patterns.length > 0) {
          const onEvent = (event: Event) => {
            if (!idAbove(event.id, snap.asOfEventId)) return;
            const fold = applyRef.current;
            if (!fold) {
              reload();
              return;
            }
            let refetch = false;
            setState((prev) => {
              if (prev.status !== "ready") return prev;
              const folded = fold(prev.data, event);
              if (folded === undefined) {
                refetch = true;
                return prev;
              }
              return { status: "ready", data: folded, error: null };
            });
            if (refetch) reload();
          };
          for (const p of patterns) unsubs.push(stream.subscribe(p, onEvent));
        }
      } catch (err) {
        if (cancelled || controller.signal.aborted) return;
        setState({
          status: "error",
          data: null,
          error: err instanceof Error ? err : new Error(String(err)),
        });
      } finally {
        for (const b of buffers) b.close();
      }
    })();

    return () => {
      cancelled = true;
      controller.abort();
      for (const b of buffers) b.close();
      for (const u of unsubs) u();
    };
  }, [patternKey, generation, epoch, reload, loader]);

  return { ...state, reload };
}

/**
 * The stream epoch. It changes when the shell reopens the stream after a reset, which is
 * every mounted snapshot's cue to reload through its registered loader.
 */
export function useStreamEpoch(): number {
  const [epoch, setEpoch] = useState(() => stream.epoch());
  useEffect(() => stream.onEpoch(() => setEpoch(stream.epoch())), []);
  return epoch;
}

/**
 * The shared connection's health, for the one indicator that says whether anything on
 * screen is still live. It reflects the actual `EventSource`, so it cannot claim "live"
 * while the browser is retrying.
 */
export function useStreamStatus(): StreamStatus {
  const [status, setStatus] = useState(() => stream.status());
  useEffect(() => {
    setStatus(stream.status());
    return stream.onStatus(setStatus);
  }, []);
  return status;
}

/** A rolling count of matching events, for a live indicator and a small history. */
export type Activity = {
  /** Events seen in the window. */
  total: number;
  /** Epoch milliseconds of the most recent match, or null if none arrived yet. */
  lastAt: number | null;
  /** The most recent matching event, or null. */
  last: Event | null;
  /** Counts per equal time slice, oldest first, ending at now. */
  buckets: number[];
};

export type UseActivityOptions = {
  /** How far back the history reaches. Defaults to ten minutes. */
  windowMs?: number;
  /** How many slices that window is cut into. Defaults to 24. */
  buckets?: number;
};

const EMPTY_ACTIVITY: Activity = { total: 0, lastAt: null, last: null, buckets: [] };

/**
 * Watches one or more patterns on the shared stream and reports arrival rate rather than
 * content. This is what makes a tile look alive without the shell knowing what any
 * plugin's events mean.
 *
 * It holds only timestamps, so the cost does not grow with payload size, and it slides the
 * window on a timer of one bucket width — the only timer in the UI, and it touches no
 * network.
 */
export function useActivity(
  patterns: string | readonly string[],
  options: UseActivityOptions = {},
): Activity {
  const { windowMs = 10 * 60_000, buckets = 24 } = options;
  const patternKey = patternList(patterns).join("\n");
  const stamps = useRef<number[]>([]);
  const last = useRef<Event | null>(null);
  const [activity, setActivity] = useState<Activity>(EMPTY_ACTIVITY);
  const epoch = useStreamEpoch();

  useEffect(() => {
    const list = patternKey === "" ? [] : patternKey.split("\n");
    stamps.current = [];
    last.current = null;
    setActivity(EMPTY_ACTIVITY);
    if (list.length === 0) return;

    const recompute = () => {
      const now = Date.now();
      const floor = now - windowMs;
      const kept = stamps.current.filter((t) => t > floor);
      stamps.current = kept;

      const width = windowMs / buckets;
      const counts = new Array<number>(buckets).fill(0);
      for (const t of kept) {
        const slot = Math.min(buckets - 1, Math.floor((t - floor) / width));
        counts[slot] = (counts[slot] ?? 0) + 1;
      }
      setActivity({
        total: kept.length,
        lastAt: kept.at(-1) ?? null,
        last: last.current,
        buckets: counts,
      });
    };

    // Two patterns can match the same event. Ids are monotonic and the stream delivers one
    // event to every subscriber before the next, so a high-water mark deduplicates exactly
    // — and, unlike a set of seen ids, does not grow without bound on a busy stream.
    let highWater = "0";
    const unsubs = list.map((p) =>
      stream.subscribe(p, (event) => {
        if (!idAbove(event.id, highWater)) return;
        highWater = event.id;
        stamps.current.push(Date.now());
        last.current = event;
        recompute();
      }),
    );

    // Draw an empty history immediately rather than leaving a blank until the first tick.
    recompute();
    const timer = setInterval(recompute, Math.max(1_000, windowMs / buckets));
    return () => {
      clearInterval(timer);
      for (const u of unsubs) u();
    };
  }, [patternKey, windowMs, buckets, epoch]);

  return activity;
}

/**
 * A clock that ticks only while something is rendering relative times. Returns epoch
 * milliseconds, re-rendering on the given interval.
 */
export function useNow(intervalMs = 1_000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(timer);
  }, [intervalMs]);
  return now;
}
