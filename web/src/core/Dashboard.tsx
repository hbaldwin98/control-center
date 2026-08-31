/**
 * The control centre's front page.
 *
 * One tile per registered plugin, in a grid, each one a live summary of what the host
 * knows — state, spend against budget, open work, recent failures — plus whatever the
 * plugin itself chooses to show. A plugin that declares live patterns gets a live
 * indicator and an activity history driven by the one shared stream; one that declares
 * nothing still gets a tile, just a static one. Clicking a tile opens that plugin.
 */
import { useCallback, useMemo } from "react";
import { Link } from "react-router-dom";
import {
  Badge,
  Callout,
  Card,
  EmptyState,
  Grid,
  LiveDot,
  Meter,
  Metric,
  Page,
  PageHeader,
  RelativeTime,
  Sparkline,
  Stack,
  api,
  formatTime,
  formatUSD,
  useActivity,
  useEvents,
  useSnapshot,
  useStreamStatus,
} from "@cc/ui";
import type { Event, PluginModule } from "@cc/ui";
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
  // Job rows carry progress that changes constantly; the events are invalidations and the
  // list refetches, which is the shape the rest of the UI already uses for jobs.
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
        {states.status === "error" ? <Callout tone="danger">{states.error.message}</Callout> : null}
        {jobs.status === "error" ? <Callout tone="danger">{jobs.error.message}</Callout> : null}

        <Grid min={168}>
          <Metric
            label="Plugins"
            value={states.status === "ready" ? `${enabledCount}/${rows.length}` : "—"}
            hint={attention > 0 ? `${attention} need attention` : "all healthy"}
            tone={attention > 0 ? "warn" : "neutral"}
          />
          <Metric
            label="Spent today"
            value={states.status === "ready" ? formatUSD(spentToday, { compact: true }) : "—"}
            hint={heldToday > 0 ? `${formatUSD(heldToday, { compact: true })} held` : "nothing held"}
          />
          <Metric
            label="Running"
            value={jobs.status === "ready" ? running.length : "—"}
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

        {states.status === "loading" ? (
          <EmptyState>Loading plugins…</EmptyState>
        ) : rows.length === 0 ? (
          <EmptyState>No plugins are registered yet.</EmptyState>
        ) : (
          <Grid min={320}>
            {rows.map((state) => (
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

        <Card title="Running now" actions={<span className="cc-field__hint">{running.length}</span>}>
          {jobs.status === "loading" ? (
            <div className="cc-field__hint">Loading…</div>
          ) : running.length === 0 ? (
            <div className="cc-field__hint">Nothing is running.</div>
          ) : (
            <table className="cc-table">
              <thead>
                <tr>
                  <th>ID</th>
                  <th>Job</th>
                  <th>Progress</th>
                  <th>Started</th>
                </tr>
              </thead>
              <tbody>
                {running.map((j) => (
                  <tr key={j.id}>
                    <td className="cc-num">
                      <Link to={`/jobs?id=${j.id}`}>{j.id}</Link>
                    </td>
                    <td>
                      <Link to={`/plugins/${encodeURIComponent(j.pluginId)}`}>{j.pluginId}</Link>
                      <span className="cc-field__hint">.{j.name}</span>
                    </td>
                    <td>
                      <div className="cc-row cc-row--tight">
                        <Meter value={j.progress} max={1} tone="accent" label={`${j.name} progress`} />
                        <span className="cc-field__hint">{Math.round(j.progress * 100)}%</span>
                      </div>
                      {j.progressMessage ? (
                        <div className="cc-field__hint">{j.progressMessage}</div>
                      ) : null}
                    </td>
                    <td className="cc-field__hint">
                      {j.startedAt ? <RelativeTime at={j.startedAt} /> : "—"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Card>

        <Card
          title="Recent alerts"
          actions={<span className="cc-field__hint">live, since this page opened</span>}
        >
          {alerts.length === 0 ? (
            <div className="cc-field__hint">No alerts yet.</div>
          ) : (
            <table className="cc-table">
              <thead>
                <tr>
                  <th>When</th>
                  <th>Type</th>
                  <th>Source</th>
                  <th>Subject</th>
                </tr>
              </thead>
              <tbody>
                {[...alerts].reverse().map((e) => (
                  <tr key={e.id}>
                    <td className="cc-field__hint" title={e.createdAt}>
                      {formatTime(e.createdAt)}
                    </td>
                    <td>
                      <code>{e.type}</code>
                    </td>
                    <td>
                      {modules.has(e.source) ? (
                        <Link to={`/plugins/${encodeURIComponent(e.source)}`}>{e.source}</Link>
                      ) : (
                        <span className="cc-field__hint">{e.source}</span>
                      )}
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
  const streamStatus = useStreamStatus();

  const verdict = verdictOf(state, failures);
  const badge = VERDICTS[verdict];
  const running = open.filter((j) => j.state === "running");
  const detail = `/plugins/${encodeURIComponent(state.pluginId)}`;
  const own = module?.nav[0];

  return (
    <Card
      className="cc-tile"
      muted={!state.enabled}
      title={
        <Link className="cc-tile__link" to={detail}>
          {state.name || state.pluginId}
        </Link>
      }
      actions={
        <LiveDot
          state={liveState(live.length > 0, streamStatus === "live", activity.lastAt)}
          label={liveLabel(live.length > 0, streamStatus === "live", activity.lastAt)}
        />
      }
    >
      <Stack>
        <div className="cc-row cc-row--tight">
          <Badge tone={badge.tone}>{badge.label}</Badge>
          {state.automated ? <Badge>automated</Badge> : null}
          <code className="cc-field__hint">{state.pluginId}</code>
        </div>

        {dashboard?.summary || state.description ? (
          <p className="cc-tile__lede">{dashboard?.summary || state.description}</p>
        ) : null}

        {!state.enabled && state.disabledReason ? (
          <div className="cc-field__hint">{state.disabledReason}</div>
        ) : null}

        <div className="cc-tile__stats">
          <div>
            <div className="cc-tile__stat-label">Today</div>
            <div className="cc-tile__stat-value">
              {formatUSD(state.committedDay, { compact: true })}
              {state.budget.daily > 0 ? (
                <span className="cc-field__hint">
                  {" / "}
                  {formatUSD(state.budget.daily, { compact: true })}
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
              <div className="cc-field__hint">no daily budget</div>
            )}
          </div>
          <div>
            <div className="cc-tile__stat-label">Work</div>
            <div className="cc-tile__stat-value">{running.length}</div>
            <div className="cc-field__hint">
              {open.length - running.length > 0
                ? `${open.length - running.length} waiting`
                : "nothing waiting"}
              {failures > 0 ? ` · ${failures} failed` : ""}
            </div>
          </div>
        </div>

        {live.length > 0 ? (
          <div className="cc-tile__activity">
            <Sparkline
              values={activity.buckets}
              label={`${state.pluginId} event activity over the last ten minutes`}
            />
            <div className="cc-field__hint">
              {activity.lastAt !== null ? (
                <>
                  <code>{activity.last?.type}</code> <RelativeTime at={activity.lastAt} />
                </>
              ) : (
                `watching ${live.join(", ")}`
              )}
            </div>
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

/** The header indicator for the one shared connection. */
function StreamIndicator() {
  const status = useStreamStatus();
  const tone =
    status === "live"
      ? "ok"
      : status === "connecting" || status === "reconnecting"
        ? "warn"
        : "danger";
  const label =
    status === "live"
      ? "live"
      : status === "connecting"
        ? "connecting"
        : status === "reconnecting"
          ? "reconnecting"
          : status === "reset"
            ? "reloading"
            : status === "offline"
              ? "offline"
              : "idle";
  return (
    <Badge tone={tone}>
      <LiveDot state={status === "live" ? "live" : "off"} label={`stream ${label}`} />
      {label}
    </Badge>
  );
}
