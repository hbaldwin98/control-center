/**
 * Shared formatters. Every screen renders money and timestamps the same way, so a
 * column means the same thing wherever an operator reads it.
 */

/** Micro-USD to a currency string. The unit the backend accounts in is never shown raw. */
export function formatUSD(microUsd: number): string {
  return (microUsd / 1_000_000).toLocaleString(undefined, {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: 2,
    maximumFractionDigits: 6,
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
