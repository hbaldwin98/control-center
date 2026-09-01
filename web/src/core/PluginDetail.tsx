/**
 * One plugin, in full.
 *
 * Overview is what it is doing: status, its own surface, recent jobs. Settings is
 * everything that used to bury that view — AI, budget, config, kill switch. An operator
 * who drilled in from a tile should not have to hunt past forms to see the plugin, and
 * should not have to navigate away to turn it off.
 */
import { useCallback, useMemo, useState } from "react";
import { Link, useLocation, useParams } from "react-router-dom";
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
  Tabs,
  api,
  formatProgress,
  useActivity,
  useSnapshot,
  useStreamStatus,
} from "@cc/ui";
import type { Activity, PluginModule, UseSnapshotResult } from "@cc/ui";
import { ConfigForm } from "./ConfigForm";
import { fieldsFromSchema } from "./configSchema";
import { PluginAI } from "./PluginAI";
import { PluginSurface } from "./PluginSurface";
import {
  BudgetForm,
  KillSwitch,
  PluginProblems,
  SpendWindows,
  budgetKey,
} from "./PluginControls";
import { liveLabel, liveState } from "./live";
import { VERDICTS } from "./status";
import { isFailure, pulseOf, verdictOf } from "./types";
import type { Job, PluginState } from "./types";

export function PluginDetail({ plugins }: { plugins: PluginModule[] }) {
  const { id = "" } = useParams();
  const settings = useLocation().pathname.replace(/\/+$/, "").endsWith("/settings");
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
  const pulse = pulseOf(jobRows);
  const badge = VERDICTS[verdictOf(state, pulse)];
  const own = module?.nav[0];
  const dashboard = module?.dashboard;
  const lede = dashboard?.summary || state.description;
  const base = `/plugins/${encodeURIComponent(id)}`;

  return (
    <Page>
      <PageHeader
        title={state.name || state.pluginId}
        {...(lede ? { lede } : {})}
        actions={
          <>
            <Badge tone={badge.tone}>{badge.label}</Badge>
            {state.automated ? <Badge>automated</Badge> : null}
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

        <Tabs label="Plugin sections">
          <Link to={base} aria-current={!settings ? "page" : undefined}>
            Overview
          </Link>
          <Link to={`${base}/settings`} aria-current={settings ? "page" : undefined}>
            Settings
          </Link>
        </Tabs>

        <PluginProblems state={state} />
        {error ? <Callout tone="danger">{error}</Callout> : null}

        {settings ? (
          <SettingsPane state={state} onSaved={states.reload} onError={setError} />
        ) : (
          <OverviewPane
            state={state}
            jobs={jobs}
            jobRows={jobRows}
            pulse={pulse}
            activity={activity}
            connected={connected}
            watched={watched}
            dashboard={dashboard}
          />
        )}
      </Stack>
    </Page>
  );
}

function OverviewPane({
  state,
  jobs,
  jobRows,
  pulse,
  activity,
  connected,
  watched,
  dashboard,
}: {
  state: PluginState;
  jobs: UseSnapshotResult<Job[]>;
  jobRows: Job[];
  pulse: ReturnType<typeof pulseOf>;
  activity: Activity;
  connected: boolean;
  watched: string[];
  dashboard: PluginModule["dashboard"];
}) {
  return (
    <>
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
          value={jobs.status === "ready" ? pulse.running : <Dash />}
          hint={pulse.waiting > 0 ? `${pulse.waiting} waiting` : "nothing waiting"}
        />
        <Metric
          label="Last job"
          value={jobs.status === "ready" ? jobRows[0]?.state ?? "none" : <Dash />}
          hint="newest of the last 50"
          tone={pulse.failing ? "warn" : "neutral"}
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

      {dashboard?.detail ? (
        <Card>
          <PluginSurface pluginId={state.pluginId} enabled={state.enabled} surface={dashboard.detail} />
        </Card>
      ) : null}

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

      <Card
        title="Jobs"
        actions={<Link to={`/jobs?plugin=${encodeURIComponent(state.pluginId)}`}>All jobs</Link>}
      >
        <Async state={jobs} loading="Loading jobs…" empty="This plugin has no jobs.">
          {() => (
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
              {jobRows.slice(0, 8).map((j) => (
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
    </>
  );
}

function SettingsPane({
  state,
  onSaved,
  onError,
}: {
  state: PluginState;
  onSaved: () => void;
  onError: (message: string | null) => void;
}) {
  return (
    <>
      <PluginAI state={state} onChanged={onSaved} />

      <Card title="Spend">
        <SpendWindows state={state} />
      </Card>

      <Card title="Budget">
        <BudgetForm
          key={budgetKey(state)}
          pluginId={state.pluginId}
          budget={state.budget}
          heading={false}
          onSaved={onSaved}
          onError={onError}
        />
      </Card>

      <ConfigCard state={state} onSaved={onSaved} onError={onError} />

      <Card title="Kill switch">
        <KillSwitch state={state} heading={false} onChanged={onSaved} onError={onError} />
      </Card>
    </>
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
