/**
 * TID — energy usage from Turlock Irrigation District.
 *
 * Syncs daily kWh from My TID (host-managed browser) or from a CSV upload, then shows
 * month-to-date metrics and a model-written insight. Imports `@cc/ui` and this directory
 * only.
 */
import { useCallback, useState } from "react";
import {
  Badge,
  Button,
  Callout,
  Card,
  Dash,
  EmptyState,
  Field,
  Grid,
  Hint,
  Loading,
  Metric,
  Page,
  PageHeader,
  PluginDisabledError,
  RelativeTime,
  Sparkline,
  Stack,
  Table,
  Textarea,
  Time,
  pluginApi,
  useSnapshot,
} from "@cc/ui";
import type { PluginModule, PluginSurfaceProps, UseSnapshotResult } from "@cc/ui";

type Day = {
  day: string;
  kwh: number;
  costCents: number | null;
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
  lastYearKwh: number;
  avg7: number;
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

function kwh(n: number): string {
  if (!Number.isFinite(n) || n === 0) return "0";
  return n.toLocaleString(undefined, { maximumFractionDigits: 1 });
}

function money(cents: number | null | undefined): string | null {
  if (cents == null) return null;
  return (cents / 100).toLocaleString(undefined, { style: "currency", currency: "USD" });
}

function delta(current: number, prior: number): string | undefined {
  if (prior <= 0) return undefined;
  const pct = ((current - prior) / prior) * 100;
  const sign = pct > 0 ? "+" : "";
  return `${sign}${pct.toFixed(0)}% vs last month`;
}

function Metrics({ data }: { data: Summary }) {
  return (
    <Grid density="metric">
      <Metric
        label="This month"
        value={`${kwh(data.monthKwh)} kWh`}
        hint={delta(data.monthKwh, data.lastMonthKwh)}
      />
      <Metric label="Last month" value={`${kwh(data.lastMonthKwh)} kWh`} />
      <Metric label="Same month last year" value={`${kwh(data.lastYearKwh)} kWh`} />
      <Metric
        label="7-day average"
        value={`${kwh(data.avg7)} kWh`}
        hint={data.peakDay ? `peak ${kwh(data.peakKwh)} on ${data.peakDay}` : undefined}
      />
      {money(data.estCostCents) ? (
        <Metric label="Month cost" value={money(data.estCostCents) ?? "—"} />
      ) : null}
    </Grid>
  );
}

function Usage() {
  const snap = useSummary();
  const [busy, setBusy] = useState<"sync" | "upload" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [csv, setCsv] = useState("");

  const run = async (kind: "sync" | "upload") => {
    setBusy(kind);
    setError(null);
    try {
      if (kind === "sync") {
        await api.post("/sync");
      } else {
        await api.post("/upload", { csv });
        setCsv("");
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(null);
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
            disabled={busy !== null || disabled}
            onClick={() => void run("sync")}
          >
            {busy === "sync" ? "Syncing…" : "Sync from My TID"}
          </Button>
        }
      />
      <Stack>
        {error ? <Callout tone="danger">{error}</Callout> : null}
        {disabled ? (
          <Callout>
            TID is disabled. Enable it on the <a href="/plugins">Plugins</a> screen, set a daily
            budget, then add your My TID username and password credential under its config.
          </Callout>
        ) : (
          <Hint>
            Create an API-key credential on <a href="/settings">Settings</a> with provider{" "}
            <code>tid</code> whose secret is your My TID password. Put that credential&apos;s id
            in this plugin&apos;s config along with your username. The plugin never sees the
            password; the host types it into the login form.
          </Hint>
        )}
        {snap.status === "error" && !disabled ? (
          <Callout tone="danger">{snap.error.message}</Callout>
        ) : null}

        {snap.status === "loading" ? (
          <Loading label="Loading usage…" />
        ) : !data || data.days.length === 0 ? (
          <EmptyState>
            No readings yet. Sync from My TID after configuring login, or paste a Usage Graphs CSV below.
          </EmptyState>
        ) : (
          <>
            <Metrics data={data} />
            <Card title="Last 30 days">
              {data.spark.length > 0 ? (
                <Sparkline values={data.spark} label="Daily kWh, last 30 days" tall />
              ) : null}
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
            <Card title="Daily readings">
              <Table
                head={
                  <>
                    <th>Day</th>
                    <th>kWh</th>
                    <th>Cost</th>
                  </>
                }
              >
                {data.days.map((d) => (
                  <tr key={d.day}>
                    <td>{d.day}</td>
                    <td>{kwh(d.kwh)}</td>
                    <td>{money(d.costCents) ?? <Dash />}</td>
                  </tr>
                ))}
              </Table>
            </Card>
          </>
        )}

        <Card title="Upload CSV">
          <Stack>
            <Hint>
              If automatic sync misses a day, open Usage Graphs on My TID, export CSV, and paste it here.
              A header row with Date and kWh is enough.
            </Hint>
            <Field label="CSV">
              <Textarea
                rows={6}
                value={csv}
                onChange={(e) => setCsv(e.target.value)}
                disabled={disabled || busy !== null}
                aria-label="Usage CSV"
              />
            </Field>
            <Button
              disabled={disabled || busy !== null || csv.trim() === ""}
              onClick={() => void run("upload")}
            >
              {busy === "upload" ? "Uploading…" : "Import readings"}
            </Button>
          </Stack>
        </Card>
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
