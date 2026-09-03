/** The shapes TID reads, and the three ways it formats a number.
 *
 * Pure: no React, no requests. Everything else in the plugin builds on this. */

export type Day = {
  day: string;
  kwh: number;
  costCents: number | null;
  onPeakKwh: number | null;
  offPeakKwh: number | null;
};

export type BillingPeriod = {
  start: string;
  end: string;
  totalKwh: number;
  totalCostCents: number | null;
  onPeakKwh: number | null;
  offPeakKwh: number | null;
  peakDemandDate: string;
  peakDemandKw: number | null;
  days: Day[];
};

export type History = {
  periods: BillingPeriod[];
  latestEventId: number;
};

export type Insight = {
  at: string;
  summary: string;
  recommendation: string;
  anomalies: string[];
};

export type LastSync = {
  at: string;
  status: string;
  rows: number;
  source: string;
  error: string;
};

export type Summary = {
  monthKwh: number;
  lastMonthKwh: number;
  lastBillingPeriodKwh: number;
  lastBillingPeriodFrom: string;
  lastBillingPeriodTo: string;
  lastYearKwh: number;
  peakDay: string;
  peakKwh: number;
  estCostCents: number | null;
  spark: number[];
  days: Day[];
  insight: Insight | null;
  lastSync: LastSync | null;
  latestEventId: number;
};

export function kwh(n: number): string {
  if (!Number.isFinite(n) || n === 0) return "0";
  return n.toLocaleString(undefined, { maximumFractionDigits: 1 });
}

export function money(cents: number | null | undefined): string | null {
  if (cents == null) return null;
  return (cents / 100).toLocaleString(undefined, { style: "currency", currency: "USD" });
}

export function delta(current: number, prior: number, label: string): string | undefined {
  if (prior <= 0) return undefined;
  const pct = ((current - prior) / prior) * 100;
  const sign = pct > 0 ? "+" : "";
  return `${sign}${pct.toFixed(0)}% vs ${label.toLowerCase()}`;
}
