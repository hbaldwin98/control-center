import { useCallback, useMemo } from "react";
import { Link } from "react-router-dom";
import {
  Async,
  Badge,
  Button,
  Dash,
  Field,
  Money,
  Page,
  PageHeader,
  Panel,
  Select,
  Stack,
  Table,
  Time,
  Toolbar,
  api,
  useQueryState,
  useSnapshot,
} from "@cc/ui";

type CallRecord = {
  id: string;
  pluginId: string;
  jobId: string;
  operation: string;
  logicalModel: string;
  status: string;
  errorClass: string;
  reservedMicroUsd: number;
  settledMicroUsd: number;
  startedAt: string;
  finalizedAt: string | null;
};

type CallPage = {
  calls: CallRecord[];
  nextAfter: string;
};

type PluginState = { pluginId: string; name?: string };

type ModelSpend = {
  logicalModel: string;
  reserved: number;
  settled: number;
  n: number;
};
type JobSpend = { jobId: string; models: ModelSpend[] };
type PluginSpend = { pluginId: string; jobs: JobSpend[] };

/** Spend by plugin, then job, then logical model. REST is the snapshot; usage events invalidate. */
export function Costs() {
  const [plugin, setPlugin] = useQueryState("plugin");
  const plugins = useSnapshot<PluginState[]>(
    useCallback(
      (signal) => api.snapshot<PluginState[]>("/api/admin/plugins", { signal }),
      [],
    ),
    { events: "core.plugin.**" },
  );
  const load = useCallback(
    (signal: AbortSignal) => {
      const q = plugin ? `?plugin=${encodeURIComponent(plugin)}` : "";
      return api.snapshot<CallPage>(`/api/ai/calls${q}`, { signal });
    },
    [plugin],
  );
  const page = useSnapshot<CallPage>(load, { events: "core.ai.usage" });
  const known = plugins.status === "ready" ? plugins.data : [];

  return (
    <Page>
      <PageHeader
        eyebrow="System / Costs"
        title="Costs"
        lede="Spend by plugin, then job, then logical model, over time."
      />
      <Stack>
        <Toolbar>
          <Field label="Plugin">
            <Select
              value={plugin}
              onChange={(e) => setPlugin(e.target.value)}
              aria-label="Filter by plugin"
            >
              <option value="">All plugins</option>
              {known.map((p) => (
                <option key={p.pluginId} value={p.pluginId}>
                  {p.name || p.pluginId}
                </option>
              ))}
            </Select>
          </Field>
          {plugin ? (
            <Button type="button" onClick={() => setPlugin("")}>
              Clear filter
            </Button>
          ) : null}
        </Toolbar>

        <Async
          state={page}
          loading="Loading AI calls…"
          empty={
            plugin
              ? "No AI calls recorded for this plugin yet."
              : "No AI calls recorded yet."
          }
          isEmpty={(p) => p.calls.length === 0}
        >
          {(data) => <Spend calls={data.calls} plugins={known} />}
        </Async>
      </Stack>
    </Page>
  );
}

function Spend({
  calls,
  plugins,
}: {
  calls: CallRecord[];
  plugins: PluginState[];
}) {
  const groups = useMemo(() => groupCalls(calls), [calls]);
  return (
    <Stack>
      {groups.map((g) => (
        <Panel key={g.pluginId} title={pluginName(plugins, g.pluginId)}>
          <Stack>
            {g.jobs.map((job) => (
              <div key={job.jobId || "none"}>
                <div className="cc-group__title">
                  {job.jobId ? (
                    <>
                      Job <Link to={`/jobs?id=${job.jobId}`}>{job.jobId}</Link>
                    </>
                  ) : (
                    "No job"
                  )}
                </div>
                <Table
                  head={
                    <>
                      <th>Model</th>
                      <th className="cc-num">Calls</th>
                      <th className="cc-num">Reserved</th>
                      <th className="cc-num">Settled</th>
                    </>
                  }
                >
                  {job.models.map((m) => (
                    <tr key={m.logicalModel}>
                      <td>
                        <code>{m.logicalModel}</code>
                      </td>
                      <td className="cc-num">{m.n}</td>
                      <td className="cc-num">
                        <Money microUsd={m.reserved} />
                      </td>
                      <td className="cc-num">
                        <Money microUsd={m.settled} />
                      </td>
                    </tr>
                  ))}
                </Table>
              </div>
            ))}
          </Stack>
        </Panel>
      ))}

      <Panel title="Calls">
        <Table
          head={
            <>
              <th>When</th>
              <th>Plugin</th>
              <th>Job</th>
              <th>Model</th>
              <th>Status</th>
              <th className="cc-num">Reserved</th>
              <th className="cc-num">Settled</th>
            </>
          }
        >
          {calls.map((c) => (
            <tr key={c.id}>
              <td>
                <Time iso={c.startedAt} />
              </td>
              <td>
                <code>{c.pluginId}</code>
              </td>
              <td>
                {c.jobId ? (
                  <Link to={`/jobs?id=${c.jobId}`}>
                    <code>{c.jobId}</code>
                  </Link>
                ) : (
                  <Dash />
                )}
              </td>
              <td>
                <code>{c.logicalModel}</code>
              </td>
              <td>
                <Badge
                  tone={
                    c.status === "succeeded"
                      ? "ok"
                      : c.status === "failed"
                        ? "danger"
                        : "neutral"
                  }
                >
                  {c.status}
                </Badge>
              </td>
              <td className="cc-num">
                <Money microUsd={c.reservedMicroUsd} />
              </td>
              <td className="cc-num">
                <Money microUsd={c.settledMicroUsd} />
              </td>
            </tr>
          ))}
        </Table>
      </Panel>
    </Stack>
  );
}

function pluginName(plugins: PluginState[], id: string): string {
  return plugins.find((p) => p.pluginId === id)?.name || id;
}

function groupCalls(calls: CallRecord[]): PluginSpend[] {
  const order: string[] = [];
  const byPlugin = new Map<string, Map<string, Map<string, ModelSpend>>>();
  for (const c of calls) {
    if (!byPlugin.has(c.pluginId)) {
      order.push(c.pluginId);
      byPlugin.set(c.pluginId, new Map());
    }
    const jobs = byPlugin.get(c.pluginId)!;
    const jobKey = c.jobId || "";
    if (!jobs.has(jobKey)) jobs.set(jobKey, new Map());
    const models = jobs.get(jobKey)!;
    const cur = models.get(c.logicalModel) ?? {
      logicalModel: c.logicalModel,
      reserved: 0,
      settled: 0,
      n: 0,
    };
    cur.reserved += c.reservedMicroUsd;
    cur.settled += c.settledMicroUsd;
    cur.n += 1;
    models.set(c.logicalModel, cur);
  }
  return order.map((pluginId) => {
    const jobs = byPlugin.get(pluginId)!;
    return {
      pluginId,
      jobs: [...jobs.entries()].map(([jobId, models]) => ({
        jobId,
        models: [...models.values()],
      })),
    };
  });
}
