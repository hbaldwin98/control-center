/**
 * TID — energy usage from Turlock Irrigation District.
 *
 * Syncs daily kWh and billing history from My TID, then shows month-to-date metrics,
 * peak/off-peak use, demand, and a model-written insight.
 */
import { useCallback, useState } from "react";
import "./index.css";
import {
  Badge,
  Button,
  Callout,
  Card,
  Dash,
  EmptyState,
  Grid,
  Hint,
  Loading,
  Metric,
  Page,
  PageHeader,
  PluginAIHint,
  PluginDisabledError,
  RelativeTime,
  Sparkline,
  Stack,
  Table,
  Time,
  pluginApi,
  useSnapshot,
} from "@cc/ui";
import type { PluginModule, PluginSurfaceProps, UseSnapshotResult } from "@cc/ui";

type Day = {
  day: string;
  kwh: number;
  costCents: number | null;
  onPeakKwh: number | null;
  offPeakKwh: number | null;
};

type BillingPeriod = {
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

type History = {
  periods: BillingPeriod[];
  latestEventId: number;
};

type Insight = {
  at: string;
  summary: string;
  recommendation: string;
  anomalies: string[];
};

type LastSync = {
  at: string;
  status: string;
  rows: number;
  source: string;
  error: string;
};

type Summary = {
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

const api = pluginApi("tid");

function useSummary(): UseSnapshotResult<Summary> {
  const load = useCallback(async (signal: AbortSignal) => {
    const data = await api.get<Summary>("/summary", signal);
    return { data, asOfEventId: String(data.latestEventId ?? 0) };
  }, []);
  return useSnapshot<Summary>(load, { events: ["tid.synced", "tid.insight"] });
}

function useHistory(): UseSnapshotResult<History> {
  const load = useCallback(async (signal: AbortSignal) => {
    const data = await api.get<History>("/history", signal);
    return { data, asOfEventId: String(data.latestEventId ?? 0) };
  }, []);
  return useSnapshot<History>(load, { events: ["tid.synced"] });
}

function kwh(n: number): string {
  if (!Number.isFinite(n) || n === 0) return "0";
  return n.toLocaleString(undefined, { maximumFractionDigits: 1 });
}

function money(cents: number | null | undefined): string | null {
  if (cents == null) return null;
  return (cents / 100).toLocaleString(undefined, { style: "currency", currency: "USD" });
}

function delta(current: number, prior: number, label: string): string | undefined {
  if (prior <= 0) return undefined;
  const pct = ((current - prior) / prior) * 100;
  const sign = pct > 0 ? "+" : "";
  return `${sign}${pct.toFixed(0)}% vs ${label.toLowerCase()}`;
}

function Metrics({ data }: { data: Summary }) {
  const [comparison, setComparison] = useState<"month" | "billing">("billing");
  const billingAvailable = data.lastBillingPeriodFrom !== "";
  const prior = comparison === "billing" && billingAvailable
    ? data.lastBillingPeriodKwh
    : data.lastMonthKwh;
  const priorLabel = comparison === "billing" && billingAvailable
    ? "Last billing period"
    : "Last month";
  return (
    <Stack>
      <div className="tid-comparison" aria-label="Usage comparison">
        <span className="cc-hint">Compare with</span>
        <Button pressed={comparison === "month"} onClick={() => setComparison("month")}>Last month</Button>
        <Button
          pressed={comparison === "billing"}
          disabled={!billingAvailable}
          onClick={() => setComparison("billing")}
        >
          Last billing period
        </Button>
      </div>
      <Grid density="metric">
        <Metric
          label="This month"
          value={`${kwh(data.monthKwh)} kWh`}
          hint={delta(data.monthKwh, prior, priorLabel)}
        />
        <Metric
          label={priorLabel}
          value={`${kwh(prior)} kWh`}
          hint={comparison === "billing" && billingAvailable
            ? `${data.lastBillingPeriodFrom} to ${data.lastBillingPeriodTo}`
            : undefined}
        />
        <Metric label="Same month last year" value={`${kwh(data.lastYearKwh)} kWh`} />
        <Metric
          label="Peak day, last 30 days"
          value={`${kwh(data.peakKwh)} kWh`}
          hint={data.peakDay || undefined}
        />
        {money(data.estCostCents) ? (
          <Metric label="Month cost" value={money(data.estCostCents) ?? "—"} />
        ) : null}
      </Grid>
    </Stack>
  );
}

function UsageChart({ days, label }: { days: Day[]; label: string }) {
  const ordered = [...days].reverse();
  const [active, setActive] = useState<number | null>(null);
  const peak = Math.max(1, ...ordered.map((day) => day.kwh));
  const selected = active == null ? null : ordered[active];
  return (
    <div className="tid-chart">
      <div className="tid-chart__header">
        <strong>{label}</strong>
        <span className="tid-chart__readout" aria-live="polite">
          {selected ? `${selected.day}: ${kwh(selected.kwh)} kWh${money(selected.costCents) ? ` · ${money(selected.costCents)}` : ""}` : "Hover or focus a day"}
        </span>
      </div>
      <div className="tid-chart__frame">
        <div className="tid-chart__axis" aria-hidden="true">
          <span>{kwh(peak)}</span>
          <span>{kwh(peak / 2)}</span>
          <span>0</span>
        </div>
        <div className="tid-chart__plot" onPointerLeave={() => setActive(null)}>
          {ordered.map((day, index) => {
            const totalHeight = Math.max(2, (day.kwh / peak) * 100);
            const hasSplit = day.onPeakKwh != null || day.offPeakKwh != null;
            const onShare = hasSplit && day.kwh > 0 ? ((day.onPeakKwh ?? 0) / day.kwh) * 100 : 0;
            return (
              <button
                className="tid-chart__day"
                key={day.day}
                type="button"
                aria-label={`${day.day}, ${kwh(day.kwh)} kilowatt-hours${day.onPeakKwh == null ? "" : `, ${kwh(day.onPeakKwh)} on peak`}${day.offPeakKwh == null ? "" : `, ${kwh(day.offPeakKwh)} off peak`}`}
                onFocus={() => setActive(index)}
                onBlur={() => setActive(null)}
                onPointerEnter={() => setActive(index)}
              >
                <span className="tid-chart__bar" style={{ height: `${totalHeight}%` }}>
                  {hasSplit ? (
                    <>
                      <span className="tid-chart__off-peak" style={{ height: `${100 - onShare}%` }} />
                      <span className="tid-chart__on-peak" style={{ height: `${onShare}%` }} />
                    </>
                  ) : (
                    <span className="tid-chart__total" />
                  )}
                </span>
              </button>
            );
          })}
        </div>
      </div>
      <div className="tid-chart__dates" aria-hidden="true">
        <span>{ordered[0]?.day}</span>
        <span>{ordered.at(-1)?.day}</span>
      </div>
      <div className="tid-chart__legend">
        <span><i className="tid-chart__swatch tid-chart__swatch--on" />On peak</span>
        <span><i className="tid-chart__swatch tid-chart__swatch--off" />Off peak</span>
      </div>
    </div>
  );
}

function BillingHistory({ disabled }: { disabled: boolean }) {
  const snap = useHistory();
  const [syncing, setSyncing] = useState(false);
  const [syncError, setSyncError] = useState<string | null>(null);
  const syncHistory = async () => {
    setSyncing(true);
    setSyncError(null);
    try {
      await api.post("/history/sync");
    } catch (err) {
      setSyncError(err instanceof Error ? err.message : String(err));
    } finally {
      setSyncing(false);
    }
  };
  if (snap.status === "loading") return <Loading label="Loading billing history…" />;
  if (snap.status === "error") return <Callout tone="danger">{snap.error.message}</Callout>;
  return (
    <Card
      title="Billing history"
      actions={
        <Button disabled={disabled || syncing} onClick={() => void syncHistory()}>
          {syncing ? "Syncing history…" : "Sync history"}
        </Button>
      }
    >
      <Stack>
        {syncError ? <Callout tone="danger">{syncError}</Callout> : null}
        <Hint>Open a billing period to inspect daily peak and off-peak use. Newest periods appear first.</Hint>
        {snap.data.periods.length === 0 ? (
          <EmptyState>Sync history to load prior billing periods.</EmptyState>
        ) : (
          <div className="tid-periods">
            {snap.data.periods.map((period, index) => (
              <details className="tid-period" key={`${period.start}:${period.end}`} open={index === 0}>
              <summary>
                <span>
                  <strong>{period.start}</strong> to <strong>{period.end}</strong>
                </span>
                <span className="tid-period__totals">
                  {kwh(period.totalKwh)} kWh
                  {money(period.totalCostCents) ? ` · ${money(period.totalCostCents)}` : ""}
                </span>
              </summary>
              <div className="tid-period__body">
                <Grid density="metric">
                  <Metric label="Total usage" value={`${kwh(period.totalKwh)} kWh`} />
                  <Metric label="On peak" value={period.onPeakKwh == null ? "—" : `${kwh(period.onPeakKwh)} kWh`} />
                  <Metric label="Off peak" value={period.offPeakKwh == null ? "—" : `${kwh(period.offPeakKwh)} kWh`} />
                  <Metric
                    label="Peak demand"
                    value={period.peakDemandKw == null ? "—" : `${kwh(period.peakDemandKw)} kW`}
                    hint={period.peakDemandDate || undefined}
                  />
                </Grid>
                <UsageChart days={period.days} label="Daily usage" />
                <Table
                  head={
                    <>
                      <th>Day</th>
                      <th>Total</th>
                      <th>On peak</th>
                      <th>Off peak</th>
                      <th>Cost</th>
                    </>
                  }
                >
                  {period.days.map((day) => (
                    <tr key={day.day}>
                      <td>{day.day}</td>
                      <td>{kwh(day.kwh)}</td>
                      <td>{day.onPeakKwh == null ? <Dash /> : kwh(day.onPeakKwh)}</td>
                      <td>{day.offPeakKwh == null ? <Dash /> : kwh(day.offPeakKwh)}</td>
                      <td>{money(day.costCents) ?? <Dash />}</td>
                    </tr>
                  ))}
                </Table>
              </div>
              </details>
            ))}
          </div>
        )}
      </Stack>
    </Card>
  );
}

function Usage() {
  const snap = useSummary();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const run = async () => {
    setBusy(true);
    setError(null);
    try {
      await api.post("/sync");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const disabled = snap.error instanceof PluginDisabledError;
  const data = snap.status === "ready" ? snap.data : null;

  return (
    <Page>
      <PageHeader
        title="TID"
        lede="Daily electricity usage from Turlock Irrigation District, with a short model reading of the last month."
        actions={
          <Button
            variant="primary"
            disabled={busy || disabled}
            onClick={() => void run()}
          >
            {busy ? "Syncing…" : "Sync from My TID"}
          </Button>
        }
      />
      <Stack>
        {error ? <Callout tone="danger">{error}</Callout> : null}
        {disabled ? (
          <Callout>
            TID is disabled. Enable it on the <a href="/plugins/tid/settings">plugin screen</a>, set a daily
            budget, then add your My TID username and password credential under its config.
          </Callout>
        ) : (
          <Hint>
            Create an API-key credential on <a href="/settings">Settings</a> with provider{" "}
            <code>tid</code> whose secret is your My TID password. Put that credential&apos;s id
            in this plugin&apos;s config along with your username. The plugin never sees the
            password; the host inserts it only into the login request.
          </Hint>
        )}
        <PluginAIHint pluginId="tid" />
        {snap.status === "error" && !disabled ? (
          <Callout tone="danger">{snap.error.message}</Callout>
        ) : null}

        {snap.status === "loading" ? (
          <Loading label="Loading usage…" />
        ) : !data || data.days.length === 0 ? (
          <EmptyState>
            No readings yet. Sync from My TID after configuring login.
          </EmptyState>
        ) : (
          <>
            <Metrics data={data} />
            <Card title="Last 30 days">
              <UsageChart days={data.days} label="Daily kWh" />
              {data.lastSync ? (
                <Hint>
                  Last {data.lastSync.source} sync{" "}
                  <RelativeTime at={data.lastSync.at} prefix="" /> · {data.lastSync.rows} days ·{" "}
                  <Badge tone={data.lastSync.status === "ok" ? "ok" : "danger"}>
                    {data.lastSync.status}
                  </Badge>
                  {data.lastSync.error ? ` — ${data.lastSync.error}` : ""}
                </Hint>
              ) : null}
            </Card>
            {data.insight ? (
              <Card title="Insight">
                <Stack>
                  <p>{data.insight.summary}</p>
                  {data.insight.recommendation ? (
                    <Hint>{data.insight.recommendation}</Hint>
                  ) : null}
                  {data.insight.anomalies.length > 0 ? (
                    <ul>
                      {data.insight.anomalies.map((a) => (
                        <li key={a}>{a}</li>
                      ))}
                    </ul>
                  ) : null}
                  <Hint>
                    <Time iso={data.insight.at} />
                  </Hint>
                </Stack>
              </Card>
            ) : null}
            <details className="cc-card">
              <summary>Daily readings</summary>
              <Table
                head={
                  <>
                    <th>Day</th>
                    <th>kWh</th>
                    <th>On peak</th>
                    <th>Off peak</th>
                    <th>Cost</th>
                  </>
                }
              >
                {data.days.map((d) => (
                  <tr key={d.day}>
                    <td>{d.day}</td>
                    <td>{kwh(d.kwh)}</td>
                    <td>{d.onPeakKwh == null ? <Dash /> : kwh(d.onPeakKwh)}</td>
                    <td>{d.offPeakKwh == null ? <Dash /> : kwh(d.offPeakKwh)}</td>
                    <td>{money(d.costCents) ?? <Dash />}</td>
                  </tr>
                ))}
              </Table>
            </details>
          </>
        )}
        {!disabled ? <BillingHistory disabled={disabled} /> : null}
      </Stack>
    </Page>
  );
}

function Tile({ enabled }: PluginSurfaceProps) {
  const snap = useSummary();
  if (snap.status === "loading") return <Hint>Loading…</Hint>;
  if (snap.status === "error") {
    return (
      <Hint>{snap.error instanceof PluginDisabledError ? "Disabled." : snap.error.message}</Hint>
    );
  }
  const { monthKwh, lastSync, insight, spark } = snap.data;
  if (!lastSync && snap.data.days.length === 0) {
    return <Hint>{enabled ? "No readings yet." : "Disabled, and no readings recorded."}</Hint>;
  }
  return (
    <Stack>
      <div className="cc-row">
        <Badge tone={enabled ? "ok" : "neutral"}>{kwh(monthKwh)} kWh this month</Badge>
        {lastSync ? (
          <span className="cc-hint">
            <RelativeTime at={lastSync.at} prefix="synced" />
          </span>
        ) : null}
      </div>
      {spark.length > 0 ? <Sparkline values={spark} label="Daily kWh" /> : null}
      {insight ? <Hint>{insight.summary}</Hint> : null}
    </Stack>
  );
}

function Detail({ enabled }: PluginSurfaceProps) {
  const snap = useSummary();
  if (snap.status !== "ready") return <Hint>Loading…</Hint>;
  if (snap.data.days.length === 0) {
    return <Hint>{enabled ? "No readings yet." : "Disabled."}</Hint>;
  }
  return (
    <Stack>
      <Metrics data={snap.data} />
      {snap.data.insight ? <Hint>{snap.data.insight.summary}</Hint> : null}
    </Stack>
  );
}

const tid: PluginModule = {
  id: "tid",
  nav: [{ path: "/tid", label: "TID" }],
  routes: [{ path: "/tid", element: <Usage /> }],
  dashboard: {
    summary: "Daily kWh from My TID, with a short reading of the last month.",
    live: ["tid.synced", "tid.insight"],
    tile: Tile,
    detail: Detail,
  },
};

export default tid;
