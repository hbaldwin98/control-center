import { useCallback, useState } from "react";
import { Badge, Card, EmptyState, Page, PageHeader, Stack, api, useSnapshot } from "@cc/ui";

type CallRecord = {
  id: string;
  pluginId: string;
  jobId: string;
  operation: string;
  logicalModel: string;
  status: string;
  errorClass: string;
  reservedMicroUsd: number;
  settledMicroUsd: number;
  startedAt: string;
  finalizedAt: string | null;
};

type CallPage = {
  calls: CallRecord[];
  nextAfter: string;
};

/** Spend by plugin, then job, then logical model. REST is the snapshot; usage events invalidate. */
export function Costs() {
  const [plugin, setPlugin] = useState("");
  const load = useCallback(
    (signal: AbortSignal) => {
      const q = plugin ? `?plugin=${encodeURIComponent(plugin)}` : "";
      return api.snapshot<CallPage>(`/api/ai/calls${q}`, { signal });
    },
    [plugin],
  );
  const page = useSnapshot<CallPage>(load, { events: "core.ai.usage" });

  const calls = page.status === "ready" ? page.data.calls : [];

  return (
    <Page>
      <PageHeader title="Costs" lede="Spend by plugin, then job, then logical model, over time." />
      <Stack>
        <label className="cc-field">
          <span className="cc-field__label">Plugin</span>
          <input
            className="cc-input"
            value={plugin}
            onChange={(e) => setPlugin(e.target.value)}
            placeholder="all plugins"
          />
        </label>

        {page.status === "error" ? <EmptyState>{page.error.message}</EmptyState> : null}
        {page.status === "loading" ? <EmptyState>Loading…</EmptyState> : null}
        {page.status === "ready" && calls.length === 0 ? (
          <EmptyState>No AI calls recorded yet.</EmptyState>
        ) : null}
        {page.status === "ready" && calls.length > 0 ? (
          <Card>
            <table className="cc-table">
              <thead>
                <tr>
                  <th>When</th>
                  <th>Plugin</th>
                  <th>Job</th>
                  <th>Model</th>
                  <th>Status</th>
                  <th>Reserved</th>
                  <th>Settled</th>
                </tr>
              </thead>
              <tbody>
                {calls.map((c) => (
                  <tr key={c.id}>
                    <td>{formatWhen(c.startedAt)}</td>
                    <td>
                      <code>{c.pluginId}</code>
                    </td>
                    <td>{c.jobId ? <code>{c.jobId}</code> : "—"}</td>
                    <td>
                      <code>{c.logicalModel}</code>
                    </td>
                    <td>
                      <Badge tone={c.status === "succeeded" ? "ok" : c.status === "failed" ? "danger" : "neutral"}>
                        {c.status}
                      </Badge>
                    </td>
                    <td className="cc-num">{formatUSD(c.reservedMicroUsd)}</td>
                    <td className="cc-num">{formatUSD(c.settledMicroUsd)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Card>
        ) : null}
      </Stack>
    </Page>
  );
}

function formatUSD(micro: number): string {
  return (micro / 1_000_000).toLocaleString(undefined, {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: 2,
    maximumFractionDigits: 6,
  });
}

function formatWhen(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}
