/**
 * Live-data hooks. A plugin gets live data from these without knowing how the transport
 * works.
 */
import { useCallback, useEffect, useRef, useState } from "react";
import { idAbove, stream } from "./stream";
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
export type ApplyEvent<T> = (current: T, event: Event) => T;

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
          if (applyRef.current) current = applyRef.current(current, event);
          else {
            // No reducer: the event is an invalidation. Refetch instead of guessing.
            for (const b of buffers) b.close();
            if (!cancelled) reload();
            return;
          }
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
            setState((prev) =>
              prev.status === "ready"
                ? { status: "ready", data: fold(prev.data, event), error: null }
                : prev,
            );
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
  }, [patternKey, generation, epoch, reload]);

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
