import { useCallback } from "react";
import { Link } from "react-router-dom";
import {
  Async,
  Badge,
  Card,
  Dash,
  EmptyState,
  Grid,
  Hint,
  Money,
  Page,
  PageHeader,
  Stack,
  Table,
  Time,
  api,
  formatProgress,
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
        <Async state={plugins} loading="Loading plugins…" empty="No plugins are registered yet.">
          {(list) => (
            <Grid>
              {list.map((p) => (
                <Card key={p.pluginId} title={p.name || p.pluginId} muted={!p.enabled}>
                  <Stack>
                    <div className="cc-row">
                      <Badge tone={p.enabled ? "ok" : "danger"}>
                        {p.enabled ? "enabled" : "disabled"}
                      </Badge>
                      <Link className="cc-mono" to="/plugins">
                        {p.pluginId}
                      </Link>
                    </div>
                    {!p.enabled && p.disabledReason ? <Hint>{p.disabledReason}</Hint> : null}
                    <dl className="cc-stats">
                      <dt>Hour</dt>
                      <dd>
                        <Money microUsd={p.reservedHour + p.committedHour} />
                      </dd>
                      <dt>Day</dt>
                      <dd>
                        <Money microUsd={p.reservedDay + p.committedDay} />
                      </dd>
                    </dl>
                  </Stack>
                </Card>
              ))}
            </Grid>
          )}
        </Async>

        <Card title="Running jobs">
          <Async state={running} loading="Loading jobs…" empty="Nothing is running.">
            {(jobs) => (
              <Table
                head={
                  <>
                    <th className="cc-num">ID</th>
                    <th>Job</th>
                    <th>Progress</th>
                  </>
                }
              >
                {jobs.map((j) => (
                  <tr key={j.id}>
                    <td className="cc-num">
                      <Link to={`/jobs?id=${j.id}`}>{j.id}</Link>
                    </td>
                    <td>
                      <code>
                        {j.pluginId}.{j.name}
                      </code>
                    </td>
                    <td className="cc-nowrap">{formatProgress(j.progress, j.progressMessage)}</td>
                  </tr>
                ))}
              </Table>
            )}
          </Async>
        </Card>

        <Card title="Recent alerts">
          {alerts.length === 0 ? (
            <EmptyState>No alerts yet.</EmptyState>
          ) : (
            <Table
              head={
                <>
                  <th>When</th>
                  <th>Type</th>
                  <th>Subject</th>
                </>
              }
            >
              {[...alerts].reverse().map((e) => (
                <tr key={e.id}>
                  <td>
                    <Time iso={e.createdAt} timeOnly />
                  </td>
                  <td>
                    <code>{e.type}</code>
                  </td>
                  <td>{e.subject || <Dash />}</td>
                </tr>
              ))}
            </Table>
          )}
        </Card>
      </Stack>
    </Page>
  );
}
