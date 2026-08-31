import { useCallback, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Badge, Card, EmptyState, Field, Page, PageHeader, Stack, api, useSnapshot } from "@cc/ui";

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

type PluginState = { pluginId: string; name?: string };

type ModelSpend = { logicalModel: string; reserved: number; settled: number; n: number };
type JobSpend = { jobId: string; models: ModelSpend[] };
type PluginSpend = { pluginId: string; jobs: JobSpend[] };

/** Spend by plugin, then job, then logical model. REST is the snapshot; usage events invalidate. */
export function Costs() {
  const [plugin, setPlugin] = useState("");
  const plugins = useSnapshot<PluginState[]>(
    useCallback((signal) => api.snapshot<PluginState[]>("/api/admin/plugins", { signal }), []),
    { events: "core.plugin.**" },
  );
  const load = useCallback(
    (signal: AbortSignal) => {
      const q = plugin ? `?plugin=${encodeURIComponent(plugin)}` : "";
      return api.snapshot<CallPage>(`/api/ai/calls${q}`, { signal });
    },
    [plugin],
  );
  const page = useSnapshot<CallPage>(load, { events: "core.ai.usage" });

  const calls = page.status === "ready" ? page.data.calls : [];
  const groups = useMemo(() => groupCalls(calls), [calls]);

  return (
    <Page>
      <PageHeader title="Costs" lede="Spend by plugin, then job, then logical model, over time." />
      <Stack>
        <Field label="Plugin">
          <select
            className="cc-input"
            value={plugin}
            onChange={(e) => setPlugin(e.target.value)}
            aria-label="Filter by plugin"
            style={{ maxWidth: 280 }}
          >
            <option value="">All plugins</option>
            {plugins.status === "ready"
              ? plugins.data.map((p) => (
                  <option key={p.pluginId} value={p.pluginId}>
                    {p.name || p.pluginId}
                  </option>
                ))
              : null}
          </select>
        </Field>

        {page.status === "error" ? <EmptyState>{page.error.message}</EmptyState> : null}
        {page.status === "loading" ? <EmptyState>Loading…</EmptyState> : null}
        {page.status === "ready" && calls.length === 0 ? (
          <EmptyState>No AI calls recorded yet.</EmptyState>
        ) : null}
        {page.status === "ready" && groups.length > 0
          ? groups.map((g) => (
              <Card key={g.pluginId} title={pluginName(plugins.status === "ready" ? plugins.data : [], g.pluginId)}>
                <Stack>
                  {g.jobs.map((job) => (
                    <div key={job.jobId || "none"}>
                      <div className="cc-group__title">
                        {job.jobId ? (
                          <>
                            Job <Link to={`/jobs?id=${job.jobId}`}>{job.jobId}</Link>
                          </>
                        ) : (
                          "No job"
                        )}
                      </div>
                      <table className="cc-table">
                        <thead>
                          <tr>
                            <th>Model</th>
                            <th>Calls</th>
                            <th>Reserved</th>
                            <th>Settled</th>
                          </tr>
                        </thead>
                        <tbody>
                          {job.models.map((m) => (
                            <tr key={m.logicalModel}>
                              <td>
                                <code>{m.logicalModel}</code>
                              </td>
                              <td className="cc-num">{m.n}</td>
                              <td className="cc-num">{formatUSD(m.reserved)}</td>
                              <td className="cc-num">{formatUSD(m.settled)}</td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  ))}
                </Stack>
              </Card>
            ))
          : null}

        {page.status === "ready" && calls.length > 0 ? (
          <Card title="Calls">
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
                    <td>
                      {c.jobId ? (
                        <Link to={`/jobs?id=${c.jobId}`}>
                          <code>{c.jobId}</code>
                        </Link>
                      ) : (
                        "—"
                      )}
                    </td>
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

function pluginName(plugins: PluginState[], id: string): string {
  return plugins.find((p) => p.pluginId === id)?.name || id;
}

function groupCalls(calls: CallRecord[]): PluginSpend[] {
  const order: string[] = [];
  const byPlugin = new Map<string, Map<string, Map<string, ModelSpend>>>();
  for (const c of calls) {
    if (!byPlugin.has(c.pluginId)) {
      order.push(c.pluginId);
      byPlugin.set(c.pluginId, new Map());
    }
    const jobs = byPlugin.get(c.pluginId)!;
    const jobKey = c.jobId || "";
    if (!jobs.has(jobKey)) jobs.set(jobKey, new Map());
    const models = jobs.get(jobKey)!;
    const cur = models.get(c.logicalModel) ?? { logicalModel: c.logicalModel, reserved: 0, settled: 0, n: 0 };
    cur.reserved += c.reservedMicroUsd;
    cur.settled += c.settledMicroUsd;
    cur.n += 1;
    models.set(c.logicalModel, cur);
  }
  return order.map((pluginId) => {
    const jobs = byPlugin.get(pluginId)!;
    return {
      pluginId,
      jobs: [...jobs.entries()].map(([jobId, models]) => ({
        jobId,
        models: [...models.values()],
      })),
    };
  });
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
