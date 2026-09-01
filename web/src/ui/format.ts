/**
 * Shared formatters. Every screen renders money and timestamps the same way, so a
 * column means the same thing wherever an operator reads it.
 */

/**
 * Micro-USD to a currency string. The unit the backend accounts in is never shown raw.
 *
 * `compact` is for a dashboard tile, where six decimals would be noise: it caps at cents
 * and says "<$0.01" rather than rounding a real charge down to nothing.
 */
export function formatUSD(microUsd: number, options: { compact?: boolean } = {}): string {
  const dollars = microUsd / 1_000_000;
  if (options.compact && dollars !== 0 && Math.abs(dollars) < 0.01) {
    return `<${(0.01).toLocaleString(undefined, { style: "currency", currency: "USD" })}`;
  }
  return dollars.toLocaleString(undefined, {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: 2,
    maximumFractionDigits: options.compact ? 2 : 6,
  });
}

/** Full local date and time. For anything that may be older than today. */
export function formatDateTime(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}

/** Local time only. For live logs, where the date is almost always today. */
export function formatTime(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleTimeString();
}

/** A progress fraction and its message as one cell of text. */
export function formatProgress(fraction: number, message: string): string {
  const pct = `${Math.round((Number.isFinite(fraction) ? fraction : 0) * 100)}%`;
  return message ? `${pct} · ${message}` : pct;
}

/**
 * Elapsed time in words: "just now", "12s ago", "4m ago".
 *
 * This is for anything whose recency is the point — when a plugin last published, when a
 * job started. Use `formatDateTime` where the instant itself matters.
 */
export function formatRelative(at: number, now = Date.now()): string {
  const seconds = Math.round((now - at) / 1000);
  if (seconds < 2) return "just now";
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.round(hours / 24)}d ago`;
}

/** Time left until an instant: "12m", "4h 12m", "ended". */
export function formatRemaining(at: number, now = Date.now()): string {
  const seconds = Math.round((at - now) / 1000);
  if (seconds <= 0) return "ended";
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  const remMin = minutes % 60;
  if (hours < 24) {
    return remMin === 0 ? `${hours}h` : `${hours}h ${remMin}m`;
  }
  const days = Math.floor(hours / 24);
  const remH = hours % 24;
  return remH === 0 ? `${days}d` : `${days}d ${remH}h`;
}
