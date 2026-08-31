/** Shared formatting, so a figure reads the same wherever it appears. */

/** Micro-USD integers are the accounting unit everywhere below the UI. */
export function formatUSD(micro: number, options: { compact?: boolean } = {}): string {
  const dollars = micro / 1_000_000;
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

/** Shared by every relative timestamp in the UI, so they all read the same way. */
export function formatRelative(at: number, now = Date.now()): string {
  const seconds = Math.round((now - at) / 1000);
  if (seconds < 0) return "just now";
  if (seconds < 2) return "just now";
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.round(hours / 24)}d ago`;
}

/** An absolute local timestamp, for titles and detail tables. */
export function formatWhen(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}

/** Just the clock part, for dense live logs where the date is implied. */
export function formatTime(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleTimeString();
}
