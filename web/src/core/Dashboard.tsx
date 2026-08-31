/**
 * The control centre's front page.
 *
 * One tile per registered plugin, in a grid, each a live summary of what the host knows —
 * state, spend against budget, open work, recent failures — plus whatever the plugin
 * itself chooses to show. A plugin that declares live patterns gets a live indicator and
 * an activity history driven by the one shared stream; one that declares nothing still
 * gets a tile, just a static one. Clicking a tile opens that plugin.
 */
import { useCallback, useMemo } from "react";
import { Link } from "react-router-dom";
import {
  Async,
  Badge,
  Card,
  Dash,
  EmptyState,
  Grid,
  Hint,
  LiveDot,
  Meter,
  Metric,
  Money,
  Page,
  PageHeader,
  RelativeTime,
  Sparkline,
  Stack,
  Table,
  Time,
  api,
  formatProgress,
  useActivity,
  useEvents,
  useSnapshot,
  useStreamStatus,
} from "@cc/ui";
import type { Event, PluginModule, StreamStatus } from "@cc/ui";
import { PluginSurface } from "./PluginSurface";
import { liveLabel, liveState } from "./live";
import { isFailure, isOpen, verdictOf } from "./types";
import type { Job, PluginState, Verdict } from "./types";

/** Events an operator should not have to go looking for. */
function isAlert(e: Event): boolean {
  return (
    e.type.endsWith(".alert") ||
    e.type === "core.browser.denied" ||
    e.type === "core.plugin.disabled" ||
    e.type === "core.plugin.budget_exceeded" ||
    e.type === "core.events.subscription_paused" ||
    e.type === "core.job.dead"
  );
}

const VERDICTS: Record<Verdict, { tone: "ok" | "warn" | "danger"; label: string }> = {
  accounting: { tone: "danger", label: "accounting failed" },
  disabled: { tone: "danger", label: "disabled" },
  degraded: { tone: "warn", label: "degraded" },
  failing: { tone: "warn", label: "failing jobs" },
  ok: { tone: "ok", label: "healthy" },
};

export function Dashboard({ plugins }: { plugins: PluginModule[] }) {
  const states = useSnapshot<PluginState[]>(
    useCallback((signal) => api.snapshot<PluginState[]>("/api/admin/plugins", { signal }), []),
    { events: ["core.plugin.**", "core.ai.usage"] },
  );
  // Job events are invalidations, so the list refetches rather than reconstructing rows
  // from partial payloads — the shape the rest of the UI already uses for jobs.
  const jobs = useSnapshot<Job[]>(
    useCallback((signal) => api.snapshot<Job[]>("/api/jobs?limit=200", { signal }), []),
    { events: "core.job.**" },
  );
  const alerts = useEvents("**", { filter: isAlert, limit: 20 });

  const modules = useMemo(() => new Map(plugins.map((m) => [m.id, m])), [plugins]);
  const rows = states.status === "ready" ? states.data : [];
  const jobRows = jobs.status === "ready" ? jobs.data : [];

  const byPlugin = useMemo(() => {
    const open = new Map<string, Job[]>();
    const failed = new Map<string, number>();
    for (const j of jobRows) {
      if (isOpen(j.state)) open.set(j.pluginId, [...(open.get(j.pluginId) ?? []), j]);
      if (isFailure(j.state)) failed.set(j.pluginId, (failed.get(j.pluginId) ?? 0) + 1);
    }
    return { open, failed };
  }, [jobRows]);

  const running = jobRows.filter((j) => j.state === "running");
  const queued = jobRows.filter((j) => j.state === "pending" || j.state === "retry_wait");
  const spentToday = rows.reduce((sum, p) => sum + p.committedDay, 0);
  const heldToday = rows.reduce((sum, p) => sum + p.reservedDay, 0);
  const enabledCount = rows.filter((p) => p.enabled).length;
  const lastAlert = alerts.at(-1);
  const attention = rows.filter(
    (p) => verdictOf(p, byPlugin.failed.get(p.pluginId) ?? 0) !== "ok",
  ).length;

  return (
    <Page>
      <PageHeader
        title="Dashboard"
        lede="Every plugin, live. Spend against budget, open work, and recent alerts. Open a tile for the full view."
        actions={<StreamIndicator />}
      />
      <Stack>
        <Grid density="metric">
          <Metric
            label="Plugins"
            value={states.status === "ready" ? `${enabledCount}/${rows.length}` : <Dash />}
            hint={attention > 0 ? `${attention} need attention` : "all healthy"}
            tone={attention > 0 ? "warn" : "neutral"}
          />
          <Metric
            label="Spent today"
            value={states.status === "ready" ? <Money microUsd={spentToday} compact /> : <Dash />}
            hint={
              heldToday > 0 ? (
                <>
                  <Money microUsd={heldToday} compact /> held
                </>
              ) : (
                "nothing held"
              )
            }
          />
          <Metric
            label="Running"
            value={jobs.status === "ready" ? running.length : <Dash />}
            hint={queued.length > 0 ? `${queued.length} waiting` : "nothing waiting"}
          />
          <Metric
            label="Alerts"
            value={alerts.length}
            hint={
              lastAlert ? (
                <RelativeTime at={lastAlert.createdAt} prefix="last" />
              ) : (
                "since this page opened"
              )
            }
            tone={alerts.length > 0 ? "warn" : "neutral"}
          />
        </Grid>

        <Async state={states} loading="Loading plugins…" empty="No plugins are registered yet.">
          {(list) => (
            <Grid density="tile">
              {list.map((state) => (
                <PluginTile
                  key={state.pluginId}
                  state={state}
                  module={modules.get(state.pluginId)}
                  open={byPlugin.open.get(state.pluginId) ?? []}
                  failures={byPlugin.failed.get(state.pluginId) ?? 0}
                />
              ))}
            </Grid>
          )}
        </Async>

        <Card title="Running now" actions={<Hint>{running.length}</Hint>}>
          <Async
            state={jobs}
            loading="Loading jobs…"
            empty="Nothing is running."
            isEmpty={() => running.length === 0}
          >
            {() => (
              <Table
                head={
                  <>
                    <th className="cc-num">ID</th>
                    <th>Job</th>
                    <th>Progress</th>
                    <th>Started</th>
                  </>
                }
              >
                {running.map((j) => (
                  <tr key={j.id}>
                    <td className="cc-num">
                      <Link to={`/jobs?id=${j.id}`}>{j.id}</Link>
                    </td>
                    <td>
                      <Link to={`/plugins/${encodeURIComponent(j.pluginId)}`}>{j.pluginId}</Link>
                      <span className="cc-hint">.{j.name}</span>
                    </td>
                    <td>
                      <Meter value={j.progress} max={1} tone="accent" label={`${j.name} progress`} />
                      <span className="cc-hint">
                        {formatProgress(j.progress, j.progressMessage ?? "")}
                      </span>
                    </td>
                    <td>{j.startedAt ? <RelativeTime at={j.startedAt} /> : <Dash />}</td>
                  </tr>
                ))}
              </Table>
            )}
          </Async>
        </Card>

        <Card title="Recent alerts" actions={<Hint>live, since this page opened</Hint>}>
          {alerts.length === 0 ? (
            <EmptyState>No alerts yet.</EmptyState>
          ) : (
            <Table
              head={
                <>
                  <th>When</th>
                  <th>Type</th>
                  <th>Source</th>
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
                  <td>
                    {modules.has(e.source) ? (
                      <Link to={`/plugins/${encodeURIComponent(e.source)}`}>{e.source}</Link>
                    ) : (
                      <code>{e.source}</code>
                    )}
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

/** One plugin's tile: host facts, the plugin's own summary, and a way in. */
function PluginTile({
  state,
  module,
  open,
  failures,
}: {
  state: PluginState;
  module: PluginModule | undefined;
  open: Job[];
  failures: number;
}) {
  const dashboard = module?.dashboard;
  const live = dashboard?.live ?? [];
  const activity = useActivity(live);
  const connected = useStreamStatus() === "live";

  const badge = VERDICTS[verdictOf(state, failures)];
  const running = open.filter((j) => j.state === "running");
  const waiting = open.length - running.length;
  const detail = `/plugins/${encodeURIComponent(state.pluginId)}`;
  const own = module?.nav[0];

  return (
    <Card
      muted={!state.enabled}
      title={
        <Link className="cc-tile__link" to={detail}>
          {state.name || state.pluginId}
        </Link>
      }
      actions={
        <LiveDot
          state={liveState(live.length > 0, connected, activity.lastAt)}
          label={liveLabel(live.length > 0, connected, activity.lastAt)}
        />
      }
    >
      <Stack>
        <div className="cc-row">
          <Badge tone={badge.tone}>{badge.label}</Badge>
          {state.automated ? <Badge>automated</Badge> : null}
          <code className="cc-hint">{state.pluginId}</code>
        </div>

        {dashboard?.summary || state.description ? (
          <Hint>{dashboard?.summary || state.description}</Hint>
        ) : null}

        {!state.enabled && state.disabledReason ? <Hint>{state.disabledReason}</Hint> : null}

        <div className="cc-tile__stats">
          <div>
            <div className="cc-tile__stat-label">Today</div>
            <div className="cc-tile__stat-value">
              <Money microUsd={state.committedDay} compact />
              {state.budget.daily > 0 ? (
                <span className="cc-hint">
                  {" / "}
                  <Money microUsd={state.budget.daily} compact />
                </span>
              ) : null}
            </div>
            {state.budget.daily > 0 ? (
              <Meter
                value={state.committedDay}
                soft={state.reservedDay}
                max={state.budget.daily}
                label={`${state.pluginId} daily budget`}
              />
            ) : (
              <Hint>no daily budget</Hint>
            )}
          </div>
          <div>
            <div className="cc-tile__stat-label">Work</div>
            <div className="cc-tile__stat-value">{running.length}</div>
            <Hint>
              {waiting > 0 ? `${waiting} waiting` : "nothing waiting"}
              {failures > 0 ? ` · ${failures} failed` : ""}
            </Hint>
          </div>
        </div>

        {live.length > 0 ? (
          <div className="cc-tile__activity">
            <Sparkline
              values={activity.buckets}
              label={`${state.pluginId} event activity over the last ten minutes`}
            />
            <Hint>
              {activity.last ? (
                <>
                  <code>{activity.last.type}</code> <RelativeTime at={activity.lastAt} />
                </>
              ) : (
                `watching ${live.join(", ")}`
              )}
            </Hint>
          </div>
        ) : null}

        <PluginSurface pluginId={state.pluginId} enabled={state.enabled} surface={dashboard?.tile} />

        <div className="cc-tile__foot">
          <Link to={detail}>Open</Link>
          {own ? <Link to={own.path}>{own.label} screen</Link> : null}
        </div>
      </Stack>
    </Card>
  );
}

const STREAM: Record<StreamStatus, { tone: "ok" | "warn" | "danger" | "neutral"; label: string }> = {
  live: { tone: "ok", label: "live" },
  connecting: { tone: "warn", label: "connecting" },
  reconnecting: { tone: "warn", label: "reconnecting" },
  reset: { tone: "warn", label: "reloading" },
  offline: { tone: "danger", label: "offline" },
  idle: { tone: "neutral", label: "idle" },
};

/**
 * The header indicator for the one shared connection.
 *
 * Every live claim on this page is downstream of it, so when it is not up the page says so
 * once, plainly, rather than leaving each tile to look healthy on stale data.
 */
function StreamIndicator() {
  const status = useStreamStatus();
  const { tone, label } = STREAM[status];
  return (
    <Badge tone={tone}>
      <LiveDot state={status === "live" ? "live" : "off"} label={`event stream ${label}`} />
      {label}
    </Badge>
  );
}
