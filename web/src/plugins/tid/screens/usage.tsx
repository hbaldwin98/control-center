/** The plugin's one screen. */
import { useState } from "react";
import {
  Badge,
  Button,
  Callout,
  Card,
  Dash,
  EmptyState,
  Hint,
  Loading,
  Page,
  PageHeader,
  PluginAIHint,
  PluginDisabledError,
  RelativeTime,
  Stack,
  Table,
  Time,
} from "@cc/ui";
import { kwh, money } from "../model";
import { api } from "../api";
import { useSummary } from "../data";
import { Metrics } from "../metrics";
import { UsageChart } from "../chart";
import { BillingHistory } from "../billing";

export function Usage() {
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
