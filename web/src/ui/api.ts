/**
 * The HTTP client every caller shares.
 *
 * Components never assemble host API URLs or security headers themselves. The shell
 * installs the session-bound CSRF token here after `/api/bootstrap`; plugin code only
 * ever sees `pluginApi(<id>)`.
 */
import { idAbove, stream } from "./stream";
import type { Event, Snapshot } from "./types";

/** The standard JSON error envelope. */
export type ApiErrorBody = {
  error: { code: string; message: string };
};

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

/** Raised for error code `plugin_disabled`. An unrelated 503 stays an ApiError. */
export class PluginDisabledError extends ApiError {
  readonly pluginId: string;

  constructor(pluginId: string, message: string) {
    super(503, "plugin_disabled", message);
    this.name = "PluginDisabledError";
    this.pluginId = pluginId;
  }
}

let csrfToken = "";

/** Called by the shell after login and after every `/api/bootstrap`. */
export function setCsrfToken(token: string): void {
  csrfToken = token;
}

export function getCsrfToken(): string {
  return csrfToken;
}

type RequestOptions = {
  method?: string;
  body?: unknown;
  signal?: AbortSignal;
  /** Set for plugin-scoped calls so a disabled plugin raises the typed error. */
  pluginId?: string;
};

async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const method = opts.method ?? "GET";
  const headers: Record<string, string> = { Accept: "application/json" };
  const mutation = method !== "GET" && method !== "HEAD";

  if (mutation) {
    headers["X-CSRF-Token"] = csrfToken;
  }
  if (opts.body !== undefined) {
    headers["Content-Type"] = "application/json";
  }

  const init: RequestInit = {
    method,
    headers,
    credentials: "same-origin",
  };
  if (opts.body !== undefined) init.body = JSON.stringify(opts.body);
  if (opts.signal) init.signal = opts.signal;

  const res = await fetch(path, init);

  if (res.status === 204) return undefined as T;

  const text = await res.text();
  const parsed: unknown = text ? safeParse(text) : undefined;

  if (!res.ok) {
    const envelope = parsed as ApiErrorBody | undefined;
    const code = envelope?.error?.code ?? "internal";
    const message = envelope?.error?.message ?? res.statusText;
    if (code === "plugin_disabled" && opts.pluginId) {
      throw new PluginDisabledError(opts.pluginId, message);
    }
    throw new ApiError(res.status, code, message);
  }
  return parsed as T;
}

function safeParse(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}

/** The host API client, used by the shell and core screens. */
export const api = {
  get: <T>(path: string, signal?: AbortSignal) =>
    request<T>(path, signal ? { signal } : {}),
  snapshot: <T>(path: string, opts: { events?: string; signal?: AbortSignal } = {}) =>
    bufferedSnapshot<T>(path, opts),
  post: <T>(path: string, body?: unknown) =>
    request<T>(path, body === undefined ? { method: "POST" } : { method: "POST", body }),
  put: <T>(path: string, body?: unknown) =>
    request<T>(path, body === undefined ? { method: "PUT" } : { method: "PUT", body }),
  del: <T>(path: string) => request<T>(path, { method: "DELETE" }),
};

/**
 * A snapshot plus whatever the stream delivered while the request was in flight.
 *
 * `pending` holds only events strictly above `asOfEventId`, in stream order — exactly the
 * deltas the snapshot does not already reflect. `useSnapshot` applies these for you; a
 * direct caller applies them itself.
 */
export type BufferedSnapshot<T> = Snapshot<T> & { pending: Event[] };

export type PluginApi = {
  get<T>(path: string, signal?: AbortSignal): Promise<T>;
  /**
   * Loads an initial state snapshot. `events` names the pattern whose stream messages are
   * buffered across the request, so nothing committed between the read and the install is
   * lost. It rides the already-open shared stream; it never opens a second connection.
   */
  snapshot<T>(
    path: string,
    opts?: { events?: string; signal?: AbortSignal },
  ): Promise<BufferedSnapshot<T>>;
  post<T>(path: string, body?: unknown): Promise<T>;
  put<T>(path: string, body?: unknown): Promise<T>;
  del<T>(path: string): Promise<T>;
};

/**
 * Runs a snapshot request with a pattern-filtered buffer open across it, then returns the
 * snapshot together with the events it does not already reflect.
 */
async function bufferedSnapshot<T>(
  path: string,
  opts: RequestOptions & { events?: string },
): Promise<BufferedSnapshot<T>> {
  const buffer = opts.events ? stream.openBuffer(opts.events) : null;
  try {
    const snap = await request<Snapshot<T>>(path, opts);
    const pending = buffer
      ? buffer.drain().filter((e) => idAbove(e.id, snap.asOfEventId))
      : [];
    return { ...snap, pending };
  } finally {
    buffer?.close();
  }
}

/**
 * A plugin-scoped client. It prefixes `/api/plugins/<id>`, sends same-origin credentials,
 * adds the current CSRF token to mutations, and parses the standard error envelope.
 */
export function pluginApi(pluginId: string): PluginApi {
  const base = `/api/plugins/${encodeURIComponent(pluginId)}`;
  const at = (path: string) => base + (path.startsWith("/") ? path : `/${path}`);

  return {
    get: <T,>(path: string, signal?: AbortSignal) =>
      request<T>(at(path), signal ? { pluginId, signal } : { pluginId }),
    snapshot: <T,>(path: string, opts: { events?: string; signal?: AbortSignal } = {}) =>
      bufferedSnapshot<T>(at(path), { ...opts, pluginId }),
    post: <T,>(path: string, body?: unknown) =>
      request<T>(at(path), body === undefined
        ? { method: "POST", pluginId }
        : { method: "POST", body, pluginId }),
    put: <T,>(path: string, body?: unknown) =>
      request<T>(at(path), body === undefined
        ? { method: "PUT", pluginId }
        : { method: "PUT", body, pluginId }),
    del: <T,>(path: string) => request<T>(at(path), { method: "DELETE", pluginId }),
  };
}
