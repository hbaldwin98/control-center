/**
 * The control centre's front page.
 *
 * It answers one question — what needs attention? — with a strip of figures, a single
 * queue of things worth opening, a short pulse of host activity, and a compact brief per
 * plugin. A plugin that declares live patterns keeps a live indicator; one that declares
 * nothing still gets a brief, just a static one. Clicking a brief opens that plugin.
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
  Metric,
  Money,
  Page,
  PageHeader,
  Panel,
  RelativeTime,
  Signal,
  Signals,
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
  const failed = jobRows.filter(
    (j) => j.state === "failed" || j.state === "dead",
  );
  const completed = jobRows.filter((j) => j.state === "succeeded");
  const spentToday = rows.reduce((sum, p) => sum + p.committedDay, 0);
  const heldToday = rows.reduce((sum, p) => sum + p.reservedDay, 0);
  const enabledCount = rows.filter((p) => p.enabled).length;
  const alerts = inbox.status === "ready" ? inbox.data.notifications : [];
  const attention = rows.filter((p) =>
    needsAttention(verdictOf(p, pulses.get(p.pluginId) ?? idlePulse)),
  ).length;

  return (
    <Page>
      <PageHeader
        eyebrow="Command / Today"
        title="Dashboard"
        lede="A single surface for plugin health, running work, and the few things worth opening first."
        actions={<StreamIndicator />}
      />
      <Stack>
        <Grid density="metric">
          <Metric
            label="Plugins online"
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
            label="Running now"
            value={jobs.status === "ready" ? running.length : <Dash />}
            hint={
              queued.length > 0 ? `${queued.length} waiting` : "nothing waiting"
            }
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
            label="Attention queue"
            value={alerts.length}
            hint={
              attention > 0
                ? `${attention} plugin${attention === 1 ? "" : "s"} flagged`
                : "nothing flagged"
            }
            tone={alerts.length > 0 ? "warn" : "neutral"}
          />
        </Grid>

        <div className="cc-layout">
          <div className="cc-layout__main">
            <DashboardFocus
              pluginStates={rows}
              pulses={pulses}
              running={running}
              waiting={queued}
              alerts={alerts}
            />
          </div>
          <div className="cc-layout__side">
            <SystemPulse
              completed={completed.length}
              running={running.length}
              spentToday={spentToday}
            />
            <NextUp
              alerts={alerts.length}
              failed={failed.length}
              plugins={rows.length}
            />
          </div>
        </div>

        <Panel
          title="Plugin pulse"
          subhead="Each plugin gets a compact operational brief, not a separate dashboard."
          actions={<Link to="/plugins">Manage plugins →</Link>}
        >
          <Async
            state={states}
            loading="Loading plugins…"
            empty="No plugins are registered yet."
          >
            {(list) => (
              <Grid density="tile">
                {list.map((state) => (
                  <PluginBrief
                    key={state.pluginId}
                    state={state}
                    module={modules.get(state.pluginId)}
                    pulse={pulses.get(state.pluginId) ?? idlePulse}
                  />
                ))}
              </Grid>
            )}
          </Async>
        </Panel>
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
    <Card
      title="Needs attention"
      subhead="The few things worth opening first."
      actions={<Link to="/inbox">Open inbox →</Link>}
    >
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

/** A short readout of host activity, the way the export's "System pulse" reads. */
function SystemPulse({
  completed,
  running,
  spentToday,
}: {
  completed: number;
  running: number;
  spentToday: number;
}) {
  const status = useStreamStatus();
  return (
    <Card
      title="System pulse"
      subhead="Recent work the host has seen."
      actions={
        <Badge tone={status === "live" ? "ok" : "warn"}>
          {status === "live" ? "stable" : "stale"}
        </Badge>
      }
    >
      <Signals>
        <Signal label="Jobs completed">{completed}</Signal>
        <Signal label="Running now">{running}</Signal>
        <Signal label="Spend today">
          <Money microUsd={spentToday} compact />
        </Signal>
      </Signals>
    </Card>
  );
}

/** The handful of links an operator reaches for after reading the queue. */
function NextUp({
  alerts,
  failed,
  plugins,
}: {
  alerts: number;
  failed: number;
  plugins: number;
}) {
  return (
    <Card title="Next up" subhead="Shortcuts into the work.">
      <Signals>
        <Signal label="Open inbox">
          <Link to="/inbox">{alerts} →</Link>
        </Signal>
        <Signal label="Review failed jobs">
          <Link to="/jobs">{failed} →</Link>
        </Signal>
        <Signal label="Manage plugins">
          <Link to="/plugins">{plugins} →</Link>
        </Signal>
      </Signals>
    </Card>
  );
}

/** One plugin's brief: name and verdict, a one-line summary, the facts, and a way in. */
function PluginBrief({
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
  const brief = dashboard?.summary || state.description;
  return (
    <Card muted={!state.enabled} className="cc-brief">
      <Stack>
        <div className="cc-brief__top">
          <Link className="cc-brief__name" to={detail}>
            {state.name || state.pluginId}
          </Link>
          <div className="cc-row">
            {live.length > 0 ? (
              <LiveDot
                state={liveState(true, connected, activity.lastAt)}
                label={liveLabel(true, connected, activity.lastAt)}
              />
            ) : null}
            <Badge tone={badge.tone}>{badge.label}</Badge>
          </div>
        </div>

        {brief ? <Hint>{brief}</Hint> : null}
        {!state.enabled && state.disabledReason ? (
          <Hint>{state.disabledReason}</Hint>
        ) : null}

        <div className="cc-brief__facts">
          <span>
            Today{" "}
            <strong>
              <Money microUsd={state.committedDay} compact />
            </strong>
            {state.budget.daily > 0 ? (
              <>
                {" / "}
                <Money microUsd={state.budget.daily} compact />
              </>
            ) : null}
          </span>
          <span>
            Running <strong>{pulse.running}</strong>
          </span>
          <span>
            {pulse.waiting > 0 ? `${pulse.waiting} waiting` : "nothing waiting"}
          </span>
          {state.automated ? <span>automated</span> : null}
        </div>

        <PluginSurface
          pluginId={state.pluginId}
          enabled={state.enabled}
          surface={dashboard?.tile}
        />

        <div className="cc-brief__foot">
          <Link to={detail}>Open plugin →</Link>
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
    <span className="cc-status-live">
      <LiveDot
        state={status === "live" ? "live" : "off"}
        label={`event stream ${label}`}
      />
      <Badge tone={tone}>{label}</Badge>
    </span>
  );
}
