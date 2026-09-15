/**
 * The control centre's front page.
 *
 * One tile per registered plugin, in a grid, each a live summary of what the host knows —
 * state, spend against budget, open work — plus whatever the plugin itself chooses to show. A plugin that declares live patterns gets a live indicator and
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
  api,
  formatProgress,
  useActivity,
  useSnapshot,
  useStreamStatus,
} from "@cc/ui";
import type { PluginModule, StreamStatus } from "@cc/ui";
import { PluginSurface } from "./PluginSurface";
import { liveLabel, liveState } from "./live";
import { VERDICTS } from "./status";
import {
  idlePulse,
  jobsByPlugin,
  needsAttention,
  pulseOf,
  verdictOf,
} from "./types";
import type { Job, JobPulse, PluginState } from "./types";

type InboxPage = {
  notifications: {
    id: string;
    title: string;
    body: string;
    subject: string;
    collapsed: number;
    createdAt: string;
    url: string;
  }[];
  nextAfter: string;
};

export function Dashboard({ plugins }: { plugins: PluginModule[] }) {
  const states = useSnapshot<PluginState[]>(
    useCallback(
      (signal) => api.snapshot<PluginState[]>("/api/admin/plugins", { signal }),
      [],
    ),
    { events: ["core.plugin.**", "core.ai.usage"] },
  );
  // Job events are invalidations, so the list refetches rather than reconstructing rows
  // from partial payloads — the shape the rest of the UI already uses for jobs.
  const jobs = useSnapshot<Job[]>(
    useCallback(
      (signal) => api.snapshot<Job[]>("/api/jobs?limit=200", { signal }),
      [],
    ),
    { events: "core.job.**" },
  );
  const inbox = useSnapshot<InboxPage>(
    useCallback(
      (signal) =>
        api.snapshot<InboxPage>("/api/notifications?limit=20", { signal }),
      [],
    ),
    { events: "core.notification.**" },
  );

  const modules = useMemo(
    () => new Map(plugins.map((m) => [m.id, m])),
    [plugins],
  );
  const rows = states.status === "ready" ? states.data : [];
  const jobRows = jobs.status === "ready" ? jobs.data : [];

  const pulses = useMemo(() => {
    const grouped = jobsByPlugin(jobRows);
    const out = new Map<string, JobPulse>();
    for (const [id, list] of grouped) out.set(id, pulseOf(list));
    return out;
  }, [jobRows]);

  const running = jobRows.filter((j) => j.state === "running");
  const queued = jobRows.filter(
    (j) => j.state === "pending" || j.state === "retry_wait",
  );
  const spentToday = rows.reduce((sum, p) => sum + p.committedDay, 0);
  const heldToday = rows.reduce((sum, p) => sum + p.reservedDay, 0);
  const enabledCount = rows.filter((p) => p.enabled).length;
  const alerts = inbox.status === "ready" ? inbox.data.notifications : [];
  const lastAlert = alerts[0];
  const attention = rows.filter((p) =>
    needsAttention(verdictOf(p, pulses.get(p.pluginId) ?? idlePulse)),
  ).length;

  return (
    <Page>
      <PageHeader
        title="Dashboard"
        lede="Start with what needs attention, then open a plugin for the full view."
        actions={<StreamIndicator />}
      />
      <Stack>
        <Grid density="metric">
          <Metric
            label="Plugins"
            value={
              states.status === "ready" ? (
                `${enabledCount}/${rows.length}`
              ) : (
                <Dash />
              )
            }
            hint={attention > 0 ? `${attention} need attention` : "all healthy"}
            tone={attention > 0 ? "warn" : "neutral"}
          />
          <Metric
            label="Spent today"
            value={
              states.status === "ready" ? (
                <Money microUsd={spentToday} compact />
              ) : (
                <Dash />
              )
            }
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
            hint={
              queued.length > 0 ? `${queued.length} waiting` : "nothing waiting"
            }
          />
          <Metric
            label="Alerts"
            value={alerts.length}
            hint={
              lastAlert ? (
                <RelativeTime at={lastAlert.createdAt} prefix="last" />
              ) : (
                "in the inbox"
              )
            }
            tone={alerts.length > 0 ? "warn" : "neutral"}
          />
        </Grid>

        {states.status === "ready" &&
        jobs.status === "ready" &&
        inbox.status === "ready" ? (
          <DashboardFocus
            pluginStates={rows}
            pulses={pulses}
            running={running}
            waiting={queued}
            alerts={alerts}
          />
        ) : null}

        <Async
          state={states}
          loading="Loading plugins…"
          empty="No plugins are registered yet."
        >
          {(list) => (
            <Grid density="tile">
              {list.map((state) => (
                <PluginTile
                  key={state.pluginId}
                  state={state}
                  module={modules.get(state.pluginId)}
                  pulse={pulses.get(state.pluginId) ?? idlePulse}
                />
              ))}
            </Grid>
          )}
        </Async>
      </Stack>
    </Page>
  );
}

/**
 * One compact queue for the things that deserve a look. The dashboard used to put
 * running jobs and alerts in separate panels below the plugin grid; putting them first
 * makes the next action obvious and avoids making the operator scan three surfaces.
 */
function DashboardFocus({
  pluginStates,
  pulses,
  running,
  waiting,
  alerts,
}: {
  pluginStates: PluginState[];
  pulses: Map<string, JobPulse>;
  running: Job[];
  waiting: Job[];
  alerts: InboxPage["notifications"];
}) {
  const flagged = pluginStates.filter((state) =>
    needsAttention(verdictOf(state, pulses.get(state.pluginId) ?? idlePulse)),
  );
  const shownRunning = running.slice(0, 5);
  const shownWaiting = waiting.slice(0, 5);
  const shownAlerts = alerts.slice(0, 5);
  const hasItems =
    flagged.length > 0 ||
    running.length > 0 ||
    waiting.length > 0 ||
    alerts.length > 0;

  return (
    <Card title="Needs attention" actions={<Link to="/inbox">Open inbox</Link>}>
      {!hasItems ? (
        <EmptyState>
          All clear. No alerts, failed plugins, or open work.
        </EmptyState>
      ) : (
        <div className="cc-focus-list">
          {flagged.map((state) => {
            const verdict =
              VERDICTS[
                verdictOf(state, pulses.get(state.pluginId) ?? idlePulse)
              ];
            const reason =
              state.accountingFailed ||
              state.health?.lastError ||
              state.disabledReason;
            return (
              <div
                className="cc-focus-list__item"
                key={`plugin:${state.pluginId}`}
              >
                <div className="cc-focus-list__main">
                  <Link to={`/plugins/${encodeURIComponent(state.pluginId)}`}>
                    {state.name || state.pluginId}
                  </Link>
                  {reason ? <Hint>{reason}</Hint> : null}
                </div>
                <Badge tone={verdict.tone}>{verdict.label}</Badge>
              </div>
            );
          })}

          {shownRunning.map((job) => (
            <div className="cc-focus-list__item" key={`job:${job.id}`}>
              <div className="cc-focus-list__main">
                <Link to={`/jobs?id=${job.id}`}>
                  {job.pluginId}.{job.name}
                </Link>
                <Hint>
                  {formatProgress(job.progress, job.progressMessage ?? "")}
                  {job.startedAt ? (
                    <span>
                      {" "}
                      · started <RelativeTime at={job.startedAt} />
                    </span>
                  ) : null}
                </Hint>
              </div>
              <Badge tone="ok">running</Badge>
            </div>
          ))}

          {shownWaiting.map((job) => (
            <div className="cc-focus-list__item" key={`waiting:${job.id}`}>
              <div className="cc-focus-list__main">
                <Link to={`/jobs?id=${job.id}`}>
                  {job.pluginId}.{job.name}
                </Link>
                <Hint>
                  {job.state === "retry_wait" ? "retrying soon" : "queued"}
                </Hint>
              </div>
              <Badge>
                {job.state === "retry_wait" ? "retrying" : "waiting"}
              </Badge>
            </div>
          ))}

          {shownAlerts.map((notification) => (
            <div
              className="cc-focus-list__item"
              key={`alert:${notification.id}`}
            >
              <div className="cc-focus-list__main">
                {notification.url ? (
                  <Link to={notification.url}>{notification.title}</Link>
                ) : (
                  <strong>{notification.title}</strong>
                )}
                {notification.body ? <Hint>{notification.body}</Hint> : null}
              </div>
              <RelativeTime at={notification.createdAt} />
            </div>
          ))}

          {running.length > shownRunning.length ? (
            <Hint>
              <Link to="/jobs">View all {running.length} running jobs</Link>
            </Hint>
          ) : null}
          {waiting.length > shownWaiting.length ? (
            <Hint>
              <Link to="/jobs">View all {waiting.length} waiting jobs</Link>
            </Hint>
          ) : null}
          {alerts.length > shownAlerts.length ? (
            <Hint>
              <Link to="/inbox">View all {alerts.length} alerts</Link>
            </Hint>
          ) : null}
        </div>
      )}
    </Card>
  );
}

/** One plugin's tile: host facts, the plugin's own summary, and a way in. */
function PluginTile({
  state,
  module,
  pulse,
}: {
  state: PluginState;
  module: PluginModule | undefined;
  pulse: JobPulse;
}) {
  const dashboard = module?.dashboard;
  const live = dashboard?.live ?? [];
  const activity = useActivity(live);
  const connected = useStreamStatus() === "live";

  const badge = VERDICTS[verdictOf(state, pulse)];
  const detail = `/plugins/${encodeURIComponent(state.pluginId)}`;
  return (
    <Card
      muted={!state.enabled}
      className="cc-tile"
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

        {!state.enabled && state.disabledReason ? (
          <Hint>{state.disabledReason}</Hint>
        ) : null}

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
            <div className="cc-tile__stat-value">{pulse.running}</div>
            <Hint>
              {pulse.waiting > 0
                ? `${pulse.waiting} waiting`
                : "nothing waiting"}
              {pulse.failing && pulse.running === 0 ? " · last job failed" : ""}
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
                  <code>{activity.last.type}</code>{" "}
                  <RelativeTime at={activity.lastAt} />
                </>
              ) : (
                `watching ${live.join(", ")}`
              )}
            </Hint>
          </div>
        ) : null}

        <PluginSurface
          pluginId={state.pluginId}
          enabled={state.enabled}
          surface={dashboard?.tile}
        />

        <div className="cc-tile__foot">
          <Link to={detail}>Open plugin</Link>
        </div>
      </Stack>
    </Card>
  );
}

const STREAM: Record<
  StreamStatus,
  { tone: "ok" | "warn" | "danger" | "neutral"; label: string }
> = {
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
      <LiveDot
        state={status === "live" ? "live" : "off"}
        label={`event stream ${label}`}
      />
      {label}
    </Badge>
  );
}
