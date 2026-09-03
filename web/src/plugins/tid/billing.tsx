/** Billing periods as they were billed, and the sync that fetches them. */
import { useState } from "react";
import {
  Button,
  Callout,
  Card,
  Dash,
  EmptyState,
  Grid,
  Hint,
  Loading,
  Metric,
  Stack,
  Table,
} from "@cc/ui";
import { kwh, money } from "./model";
import { api } from "./api";
import { useHistory } from "./data";
import { UsageChart } from "./chart";

export function BillingHistory({ disabled }: { disabled: boolean }) {
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
