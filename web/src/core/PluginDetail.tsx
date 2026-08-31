/**
 * One plugin, in full: what it is doing right now, what it has spent, the work it owns,
 * its live event feed, and every control the Plugins screen offers.
 *
 * This is where a dashboard tile leads. Everything above the administrative controls is
 * live off the one shared stream; a plugin that supplies a `detail` surface has it
 * rendered first, behind an error boundary, so a broken plugin view never costs the
 * operator access to the kill switch below it.
 */
import { useCallback, useMemo, useState } from "react";
import { Link, useParams } from "react-router-dom";
import {
  Badge,
  Callout,
  Card,
  EmptyState,
  Grid,
  LiveDot,
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
import { ConfigForm } from "./ConfigForm";
import { fieldsFromSchema } from "./configSchema";
import { PluginSurface } from "./PluginSurface";
import {
  BudgetForm,
  KillSwitch,
  PluginBadges,
  PluginProblems,
  SpendWindows,
  budgetKey,
} from "./PluginControls";
import { liveLabel, liveState } from "./live";
import { isFailure, isOpen } from "./types";
import type { Job, PluginState } from "./types";

export function PluginDetail({ plugins }: { plugins: PluginModule[] }) {
  const { id = "" } = useParams();
  const [error, setError] = useState<string | null>(null);

  const states = useSnapshot<PluginState[]>(
    useCallback((signal) => api.snapshot<PluginState[]>("/api/admin/plugins", { signal }), []),
    { events: ["core.plugin.**", "core.ai.usage"] },
  );
  const jobs = useSnapshot<Job[]>(
    useCallback(
      (signal) =>
        api.snapshot<Job[]>(`/api/jobs?plugin=${encodeURIComponent(id)}&limit=50`, { signal }),
      [id],
    ),
    { events: "core.job.**" },
  );

  const module = plugins.find((m) => m.id === id);
  const state = states.status === "ready" ? states.data.find((p) => p.pluginId === id) : undefined;

  // The host knows which events a plugin published: `source` is its id. That holds whether
  // or not the plugin declared anything, so this feed works for every plugin.
  const filter = useCallback((e: Event) => e.source === id, [id]);
  const feed = useEvents("**", { filter, limit: 60 });
  const declared = useMemo(() => module?.dashboard?.live ?? [], [module]);
  const watched = declared.length > 0 ? declared : [`${id}.**`];
  const activity = useActivity(watched);
  const streamStatus = useStreamStatus();

  if (states.status === "loading") {
    return (
      <Page>
        <PageHeader title={id} />
        <EmptyState>Loading…</EmptyState>
      </Page>
    );
  }

  if (states.status === "error") {
    return (
      <Page>
        <PageHeader title={id} />
        <Callout tone="danger">{states.error.message}</Callout>
      </Page>
    );
  }

  if (!state) {
    return (
      <Page>
        <PageHeader title={id || "Plugin"} />
        <EmptyState>
          No plugin is registered as <code>{id}</code>. <Link to="/plugins">All plugins</Link>
        </EmptyState>
      </Page>
    );
  }

  const jobRows = jobs.status === "ready" ? jobs.data : [];
  const open = jobRows.filter((j) => isOpen(j.state));
  const running = open.filter((j) => j.state === "running");
  const failures = jobRows.filter((j) => isFailure(j.state)).length;
  const own = module?.nav[0];
  const dashboard = module?.dashboard;
  const lede = dashboard?.summary || state.description;

  return (
    <Page>
      <PageHeader
        title={state.name || state.pluginId}
        {...(lede ? { lede } : {})}
        actions={
          <>
            <PluginBadges state={state} />
            {own ? (
              <Link className="cc-button" to={own.path}>
                Open {own.label}
              </Link>
            ) : null}
          </>
        }
      />

      <Stack>
        <div className="cc-row cc-row--tight">
          <code className="cc-field__hint">{state.pluginId}</code>
          <span className="cc-field__hint">·</span>
          <Link className="cc-field__hint" to="/plugins">
            All plugins
          </Link>
        </div>

        <PluginProblems state={state} />
        {error ? <Callout tone="danger">{error}</Callout> : null}
        {jobs.status === "error" ? <Callout tone="danger">{jobs.error.message}</Callout> : null}

        <Grid min={168}>
          <Metric
            label="Spent today"
            value={formatUSD(state.committedDay, { compact: true })}
            hint={
              state.reservedDay > 0
                ? `${formatUSD(state.reservedDay, { compact: true })} held`
                : state.budget.daily > 0
                  ? `of ${formatUSD(state.budget.daily, { compact: true })}`
                  : "no daily budget"
            }
          />
          <Metric
            label="Running"
            value={running.length}
            hint={
              open.length - running.length > 0
                ? `${open.length - running.length} waiting`
                : "nothing waiting"
            }
          />
          <Metric
            label="Failed jobs"
            value={failures}
            hint="in the last 50"
            tone={failures > 0 ? "warn" : "neutral"}
          />
          <Metric
            label="Events"
            value={activity.total}
            hint={
              activity.lastAt !== null ? (
                <RelativeTime at={activity.lastAt} prefix="last" />
              ) : (
                "in the last ten minutes"
              )
            }
          />
        </Grid>

        <Card
          title="Activity"
          actions={
            <span className="cc-row cc-row--tight">
              <LiveDot
                state={liveState(true, streamStatus === "live", activity.lastAt)}
                label={liveLabel(true, streamStatus === "live", activity.lastAt)}
              />
              <span className="cc-field__hint">{watched.join(", ")}</span>
            </span>
          }
        >
          <Sparkline
            values={activity.buckets}
            label={`${state.pluginId} event activity over the last ten minutes`}
            height={34}
          />
        </Card>

        {dashboard?.detail ? (
          <Card
            title={`${state.name || state.pluginId}'s view`}
            actions={
              declared.length > 0 ? <Badge tone="ok">live</Badge> : <Badge>not declared live</Badge>
            }
          >
            <PluginSurface
              pluginId={state.pluginId}
              enabled={state.enabled}
              surface={dashboard.detail}
            />
          </Card>
        ) : null}

        <Card title="Jobs" actions={<Link to={`/jobs?plugin=${encodeURIComponent(id)}`}>All jobs</Link>}>
          {jobs.status === "loading" ? (
            <div className="cc-field__hint">Loading…</div>
          ) : jobRows.length === 0 ? (
            <div className="cc-field__hint">This plugin has no jobs.</div>
          ) : (
            <table className="cc-table">
              <thead>
                <tr>
                  <th>ID</th>
                  <th>Name</th>
                  <th>State</th>
                  <th>Progress</th>
                  <th>When</th>
                </tr>
              </thead>
              <tbody>
                {jobRows.slice(0, 12).map((j) => (
                  <tr key={j.id}>
                    <td className="cc-num">
                      <Link to={`/jobs?id=${j.id}`}>{j.id}</Link>
                    </td>
                    <td>
                      <code>{j.name}</code>
                    </td>
                    <td>
                      <Badge tone={isFailure(j.state) ? "danger" : j.state === "running" ? "ok" : "neutral"}>
                        {j.state}
                      </Badge>
                    </td>
                    <td>
                      {j.state === "running" ? `${Math.round(j.progress * 100)}%` : "—"}
                      {j.progressMessage ? (
                        <div className="cc-field__hint">{j.progressMessage}</div>
                      ) : null}
                    </td>
                    <td className="cc-field__hint">
                      <RelativeTime at={j.finishedAt ?? j.startedAt ?? j.createdAt} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Card>

        <Card
          title="Live events"
          actions={<span className="cc-field__hint">since this page opened</span>}
        >
          {feed.length === 0 ? (
            <div className="cc-field__hint">
              Nothing from <code>{state.pluginId}</code> yet.
            </div>
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
                {[...feed].reverse().map((e) => (
                  <tr key={e.id}>
                    <td className="cc-field__hint" title={e.createdAt}>
                      {formatTime(e.createdAt)}
                    </td>
                    <td>
                      <code>{e.type}</code>
                    </td>
                    <td className="cc-truncate">{e.subject || "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Card>

        <Card title="Spend">
          <SpendWindows state={state} />
        </Card>

        <Card title="Budget">
          <BudgetForm
            key={budgetKey(state)}
            pluginId={state.pluginId}
            budget={state.budget}
            onSaved={states.reload}
            onError={setError}
          />
        </Card>

        <ConfigCard state={state} onSaved={states.reload} onError={setError} />

        <Card title="Kill switch">
          <Stack>
            <p className="cc-field__hint">
              Disable rejects new jobs, AI calls, event handlers, plugin HTTP, publications, and
              storage writes, and closes this plugin's browser sessions. In-process code that
              ignores cancellation is not killed.
            </p>
            <KillSwitch state={state} onChanged={states.reload} onError={setError} />
          </Stack>
        </Card>
      </Stack>
    </Page>
  );
}

/** Config is only a card when the plugin actually declares a schema. */
function ConfigCard({
  state,
  onSaved,
  onError,
}: {
  state: PluginState;
  onSaved: () => void;
  onError: (message: string | null) => void;
}) {
  if (fieldsFromSchema(state.configSchema).length === 0) return null;
  return (
    <Card title="Configuration">
      <ConfigForm
        key={`${state.pluginId}:${JSON.stringify(state.config ?? null)}`}
        pluginId={state.pluginId}
        schema={state.configSchema}
        value={state.config}
        onSaved={onSaved}
        onError={onError}
      />
    </Card>
  );
}
