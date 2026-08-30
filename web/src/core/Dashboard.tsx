import { useCallback } from "react";
import {
  Badge,
  Card,
  EmptyState,
  Grid,
  Page,
  PageHeader,
  Stack,
  api,
  useSnapshot,
} from "@cc/ui";

type PluginState = {
  pluginId: string;
  enabled: boolean;
  reservedHour: number;
  committedHour: number;
  reservedDay: number;
  committedDay: number;
  disabledReason: string;
};

type Job = {
  id: number;
  pluginId: string;
  name: string;
  state: string;
  progress: number;
  progressMessage: string;
};

/** Per-plugin state, spend today and this hour, and running jobs. */
export function Dashboard() {
  const plugins = useSnapshot<PluginState[]>(
    useCallback((signal) => api.snapshot<PluginState[]>("/api/admin/plugins", { signal }), []),
    { events: "core.plugin.**" },
  );
  const running = useSnapshot<Job[]>(
    useCallback((signal) => api.snapshot<Job[]>("/api/jobs?state=running", { signal }), []),
    { events: "core.job.**" },
  );

  return (
    <Page>
      <PageHeader title="Dashboard" lede="Per-plugin state, spend today and this hour, and work that is running now." />
      <Stack>
        {plugins.status === "ready" && plugins.data.length === 0 ? (
          <EmptyState>No plugins are registered yet.</EmptyState>
        ) : null}
        {plugins.status === "ready" && plugins.data.length > 0 ? (
          <Grid>
            {plugins.data.map((p) => (
              <Card key={p.pluginId} title={p.pluginId} muted={!p.enabled}>
                <Badge tone={p.enabled ? "ok" : "danger"}>{p.enabled ? "enabled" : "disabled"}</Badge>
                {!p.enabled && p.disabledReason ? (
                  <div className="cc-field__hint">{p.disabledReason}</div>
                ) : null}
                <dl className="cc-stats" style={{ marginTop: 10 }}>
                  <dt>Hour</dt>
                  <dd>{formatUSD(p.reservedHour + p.committedHour)}</dd>
                  <dt>Day</dt>
                  <dd>{formatUSD(p.reservedDay + p.committedDay)}</dd>
                </dl>
              </Card>
            ))}
          </Grid>
        ) : null}

        <Card title="Running jobs">
          {running.status === "loading" ? (
            <div className="cc-field__hint">Loading…</div>
          ) : running.status === "ready" && running.data.length === 0 ? (
            <div className="cc-field__hint">Nothing is running.</div>
          ) : running.status === "ready" ? (
            <table className="cc-table">
              <thead>
                <tr>
                  <th>ID</th>
                  <th>Job</th>
                  <th>Progress</th>
                </tr>
              </thead>
              <tbody>
                {running.data.map((j) => (
                  <tr key={j.id}>
                    <td className="cc-num">{j.id}</td>
                    <td>
                      <code>
                        {j.pluginId}.{j.name}
                      </code>
                    </td>
                    <td>
                      {Math.round(j.progress * 100)}%{j.progressMessage ? ` · ${j.progressMessage}` : ""}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : null}
        </Card>
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
