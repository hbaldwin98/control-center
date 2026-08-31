import { useCallback } from "react";
import { Link } from "react-router-dom";
import {
  Badge,
  Card,
  EmptyState,
  Grid,
  Page,
  PageHeader,
  Stack,
  api,
  useEvents,
  useSnapshot,
  type Event,
} from "@cc/ui";

type PluginState = {
  pluginId: string;
  name?: string;
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

function isAlert(e: Event): boolean {
  return (
    e.type.endsWith(".alert") ||
    e.type === "core.browser.denied" ||
    e.type === "core.plugin.disabled" ||
    e.type === "core.plugin.budget_exceeded"
  );
}

/** Per-plugin state, spend today and this hour, running jobs, and recent alerts. */
export function Dashboard() {
  const plugins = useSnapshot<PluginState[]>(
    useCallback((signal) => api.snapshot<PluginState[]>("/api/admin/plugins", { signal }), []),
    { events: ["core.plugin.**", "core.ai.usage"] },
  );
  const running = useSnapshot<Job[]>(
    useCallback((signal) => api.snapshot<Job[]>("/api/jobs?state=running", { signal }), []),
    { events: "core.job.**" },
  );
  const alerts = useEvents("**", { filter: isAlert, limit: 12 });

  return (
    <Page>
      <PageHeader
        title="Dashboard"
        lede="Per-plugin state, spend today and this hour, work that is running now, and recent alerts."
      />
      <Stack>
        {plugins.status === "ready" && plugins.data.length === 0 ? (
          <EmptyState>No plugins are registered yet.</EmptyState>
        ) : null}
        {plugins.status === "ready" && plugins.data.length > 0 ? (
          <Grid>
            {plugins.data.map((p) => (
              <Card key={p.pluginId} title={p.name || p.pluginId} muted={!p.enabled}>
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
                <div className="cc-field__hint" style={{ marginTop: 8 }}>
                  <Link to="/plugins">{p.pluginId}</Link>
                </div>
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
                    <td className="cc-num">
                      <Link to={`/jobs?id=${j.id}`}>{j.id}</Link>
                    </td>
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

        <Card title="Recent alerts">
          {alerts.length === 0 ? (
            <div className="cc-field__hint">No alerts yet.</div>
          ) : (
            <table className="cc-table">
              <thead>
                <tr>
                  <th>When</th>
                  <th>Type</th>
                  <th>Subject</th>
                </tr>
              </thead>
              <tbody>
                {[...alerts].reverse().map((e) => (
                  <tr key={e.id}>
                    <td title={e.createdAt}>{formatWhen(e.createdAt)}</td>
                    <td>
                      <code>{e.type}</code>
                    </td>
                    <td>{e.subject || "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
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

function formatWhen(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleTimeString();
}
