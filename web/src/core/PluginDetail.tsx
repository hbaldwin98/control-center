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
  Async,
  Badge,
  Callout,
  Card,
  Dash,
  EmptyState,
  Grid,
  Hint,
  LiveDot,
  Loading,
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
import type { Event, PluginModule } from "@cc/ui";
import { ConfigForm } from "./ConfigForm";
import { fieldsFromSchema } from "./configSchema";
import { PluginAI } from "./PluginAI";
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
  const connected = useStreamStatus() === "live";

  if (states.status === "loading") {
    return (
      <Page>
        <PageHeader title={id} />
        <Loading label="Loading plugin…" />
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
  const waiting = open.length - running.length;
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
        <Hint>
          <code>{state.pluginId}</code> · <Link to="/plugins">All plugins</Link>
        </Hint>

        <PluginProblems state={state} />
        {error ? <Callout tone="danger">{error}</Callout> : null}

        <PluginAI state={state} onChanged={states.reload} />

        <Grid density="metric">
          <Metric
            label="Spent today"
            value={<Money microUsd={state.committedDay} compact />}
            hint={
              state.reservedDay > 0 ? (
                <>
                  <Money microUsd={state.reservedDay} compact /> held
                </>
              ) : state.budget.daily > 0 ? (
                <>
                  of <Money microUsd={state.budget.daily} compact />
                </>
              ) : (
                "no daily budget"
              )
            }
          />
          <Metric
            label="Running"
            value={jobs.status === "ready" ? running.length : <Dash />}
            hint={waiting > 0 ? `${waiting} waiting` : "nothing waiting"}
          />
          <Metric
            label="Failed jobs"
            value={jobs.status === "ready" ? failures : <Dash />}
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
            <>
              <LiveDot
                state={liveState(true, connected, activity.lastAt)}
                label={liveLabel(true, connected, activity.lastAt)}
              />
              <code className="cc-hint">{watched.join(", ")}</code>
            </>
          }
        >
          <Sparkline
            values={activity.buckets}
            label={`${state.pluginId} event activity over the last ten minutes`}
            tall
          />
        </Card>

        {dashboard?.detail ? (
          <Card
            title={`${state.name || state.pluginId} view`}
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

        <Card
          title="Jobs"
          actions={<Link to={`/jobs?plugin=${encodeURIComponent(id)}`}>All jobs</Link>}
        >
          <Async state={jobs} loading="Loading jobs…" empty="This plugin has no jobs.">
            {(list) => (
              <Table
                head={
                  <>
                    <th className="cc-num">ID</th>
                    <th>Name</th>
                    <th>State</th>
                    <th>Progress</th>
                    <th>When</th>
                  </>
                }
              >
                {list.slice(0, 12).map((j) => (
                  <tr key={j.id}>
                    <td className="cc-num">
                      <Link to={`/jobs?id=${j.id}`}>{j.id}</Link>
                    </td>
                    <td>
                      <code>{j.name}</code>
                    </td>
                    <td>
                      <Badge
                        tone={
                          isFailure(j.state) ? "danger" : j.state === "running" ? "ok" : "neutral"
                        }
                      >
                        {j.state}
                      </Badge>
                    </td>
                    <td>
                      {j.state === "running" ? (
                        formatProgress(j.progress, j.progressMessage ?? "")
                      ) : j.progressMessage ? (
                        j.progressMessage
                      ) : (
                        <Dash />
                      )}
                    </td>
                    <td>
                      <RelativeTime at={j.finishedAt ?? j.startedAt ?? j.createdAt} />
                    </td>
                  </tr>
                ))}
              </Table>
            )}
          </Async>
        </Card>

        <Card title="Live events" actions={<Hint>since this page opened</Hint>}>
          {feed.length === 0 ? (
            <EmptyState>
              Nothing from <code>{state.pluginId}</code> yet.
            </EmptyState>
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
              {[...feed].reverse().map((e) => (
                <tr key={e.id}>
                  <td>
                    <Time iso={e.createdAt} timeOnly />
                  </td>
                  <td>
                    <code>{e.type}</code>
                  </td>
                  <td className="cc-truncate">{e.subject || <Dash />}</td>
                </tr>
              ))}
            </Table>
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
            heading={false}
            onSaved={states.reload}
            onError={setError}
          />
        </Card>

        <ConfigCard state={state} onSaved={states.reload} onError={setError} />

        <Card title="Kill switch">
          <KillSwitch state={state} heading={false} onChanged={states.reload} onError={setError} />
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
