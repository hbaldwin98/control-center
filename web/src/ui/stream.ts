/**
 * The one shared event stream.
 *
 * Everything live in the UI rides this connection: job progress and log invalidations,
 * cost tickers, plugin state changes, notification invalidations. REST provides initial
 * snapshots and mutations; SSE applies later changes. There is no second realtime
 * mechanism and no polling.
 */
import { matchesPattern } from "./pattern";
import type { Event } from "./types";

type Listener = (event: Event) => void;

type Subscriber = {
  pattern: string;
  listener: Listener;
};

/** A pattern-filtered buffer held open across a snapshot request. */
export type StreamBuffer = {
  /** Events seen since the buffer opened, in stream order. */
  drain(): Event[];
  close(): void;
};

class EventStream {
  #source: EventSource | null = null;
  #subscribers = new Set<Subscriber>();
  #buffers = new Set<{ pattern: string; events: Event[] }>();
  #resetHandlers = new Set<(oldestRetainedId: string) => void>();
  #epoch = 0;
  #epochHandlers = new Set<() => void>();
  #lastId = 0n;
  /** True between a reset signal and the shell reopening after a fresh bootstrap. */
  #paused = false;

  /**
   * Opens the stream at the boundary a snapshot established. Events at or below
   * `afterEventId` are already reflected in that snapshot.
   */
  start(afterEventId: string): void {
    this.stop();
    this.#paused = false;
    this.#lastId = toBigInt(afterEventId);

    const source = new EventSource(`/api/stream?after=${encodeURIComponent(afterEventId)}`, {
      withCredentials: true,
    });
    this.#source = source;

    source.addEventListener("event", (ev) => {
      if (this.#paused) return;
      const event = parseEvent((ev as MessageEvent<string>).data);
      if (!event) return;

      // The browser replays from Last-Event-ID on automatic reconnect, so the same event
      // can arrive twice. Suppress anything at or below the high-water mark.
      const id = toBigInt(event.id);
      if (id <= this.#lastId) return;
      this.#lastId = id;

      this.#deliver(event);
    });

    source.addEventListener("reset", (ev) => {
      // The requested position predates retention. Stop applying deltas and let the shell
      // reload from a fresh bootstrap rather than showing a partial history.
      const data = safeParse<{ oldestRetainedId?: string }>((ev as MessageEvent<string>).data);
      this.#paused = true;
      for (const handler of [...this.#resetHandlers]) {
        handler(data?.oldestRetainedId ?? "0");
      }
    });
  }

  stop(): void {
    this.#source?.close();
    this.#source = null;
  }

  /** The highest event id applied so far, as a decimal string. */
  position(): string {
    return this.#lastId.toString();
  }

  subscribe(pattern: string, listener: Listener): () => void {
    const sub: Subscriber = { pattern, listener };
    this.#subscribers.add(sub);
    return () => this.#subscribers.delete(sub);
  }

  /**
   * Starts collecting matching events before a snapshot request is sent, so nothing
   * committed between the server's read and the client's install is lost. It rides the
   * already-open stream; it never opens another connection.
   */
  openBuffer(pattern: string): StreamBuffer {
    const entry = { pattern, events: [] as Event[] };
    this.#buffers.add(entry);
    return {
      drain: () => entry.events.splice(0, entry.events.length),
      close: () => this.#buffers.delete(entry),
    };
  }

  onReset(handler: (oldestRetainedId: string) => void): () => void {
    this.#resetHandlers.add(handler);
    return () => this.#resetHandlers.delete(handler);
  }

  /**
   * Bumped by the shell once it has reloaded bootstrap and reopened the stream after a
   * reset. Every mounted snapshot reloads through its registered loader, so no screen is
   * left showing state from before the discontinuity.
   */
  bumpEpoch(): void {
    this.#epoch++;
    for (const handler of [...this.#epochHandlers]) handler();
  }

  epoch(): number {
    return this.#epoch;
  }

  onEpoch(handler: () => void): () => void {
    this.#epochHandlers.add(handler);
    return () => this.#epochHandlers.delete(handler);
  }

  #deliver(event: Event): void {
    for (const buffer of this.#buffers) {
      if (matchesPattern(buffer.pattern, event.type)) buffer.events.push(event);
    }
    for (const sub of [...this.#subscribers]) {
      if (matchesPattern(sub.pattern, event.type)) sub.listener(event);
    }
  }
}

/** The single stream instance. Plugins reach it only through `useEvents`. */
export const stream = new EventStream();

/** Compares two decimal int64 ids. Never parse an event id as a Number. */
export function idAbove(id: string, boundary: string): boolean {
  return toBigInt(id) > toBigInt(boundary);
}

function toBigInt(value: string): bigint {
  try {
    return BigInt(value);
  } catch {
    return 0n;
  }
}

function parseEvent(data: string): Event | null {
  const parsed = safeParse<Event>(data);
  if (!parsed || typeof parsed.id !== "string" || typeof parsed.type !== "string") return null;
  return parsed;
}

function safeParse<T>(text: string): T | null {
  try {
    return JSON.parse(text) as T;
  } catch {
    return null;
  }
}
