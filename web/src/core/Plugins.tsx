/**
 * Plugin administration: a catalog of cards, one per plugin.
 *
 * Clicking a card opens that plugin. Enable, budget, AI, and config live on the
 * plugin's own settings tab — stacking every control here made the page unusable.
 */
import { useCallback, useMemo } from "react";
import { Link } from "react-router-dom";
import {
  Async,
  Badge,
  Card,
  Grid,
  Hint,
  Meter,
  Money,
  Page,
  PageHeader,
  Stack,
  api,
  useSnapshot,
} from "@cc/ui";
import { modelsReady } from "./aiSetup";
import { VERDICTS } from "./status";
import { idlePulse, jobsByPlugin, pulseOf, verdictOf } from "./types";
import type { Job, JobPulse, PluginState } from "./types";

export function Plugins() {
  const load = useCallback(
    (signal: AbortSignal) => api.snapshot<PluginState[]>("/api/admin/plugins", { signal }),
    [],
  );
  const plugins = useSnapshot<PluginState[]>(load, { events: ["core.plugin.**", "core.ai.usage"] });
  const jobs = useSnapshot<Job[]>(
    useCallback((signal) => api.snapshot<Job[]>("/api/jobs?limit=200", { signal }), []),
    { events: "core.job.**" },
  );

  const pulses = useMemo(() => {
    const grouped = jobs.status === "ready" ? jobsByPlugin(jobs.data) : new Map<string, Job[]>();
    const out = new Map<string, JobPulse>();
    for (const [id, list] of grouped) out.set(id, pulseOf(list));
    return out;
  }, [jobs]);

  return (
    <Page>
      <PageHeader
        eyebrow="System / Plugins"
        title="Plugin registry"
        lede="Capabilities are modular, but their health should read as one system."
      />
      <Async state={plugins} loading="Loading plugins…" empty="No plugins are registered yet.">
        {(list) => (
          <Grid density="tile">
            {list.map((st) => (
              <CatalogCard key={st.pluginId} state={st} pulse={pulses.get(st.pluginId) ?? idlePulse} />
            ))}
          </Grid>
        )}
      </Async>
    </Page>
  );
}

function CatalogCard({ state, pulse }: { state: PluginState; pulse: JobPulse }) {
  const href = `/plugins/${encodeURIComponent(state.pluginId)}`;
  const badge = VERDICTS[verdictOf(state, pulse)];
  const aiReady = modelsReady(state.models);
  const lede = state.description || state.pluginId;

  return (
    <Link className="cc-catalog" to={href}>
      <Card
        muted={!state.enabled}
        className="cc-tile"
        title={state.name || state.pluginId}
        actions={<Badge tone={badge.tone}>{badge.label}</Badge>}
      >
        <Stack>
          <Hint>
            <code>{state.pluginId}</code>
          </Hint>
          <p className="cc-hint cc-catalog__lede">{lede}</p>
          {!aiReady ? <Badge tone="warn">needs AI setup</Badge> : null}
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
                {pulse.waiting > 0 ? `${pulse.waiting} waiting` : "nothing waiting"}
                {pulse.failing && pulse.running === 0 ? " · last job failed" : ""}
              </Hint>
            </div>
          </div>
        </Stack>
      </Card>
    </Link>
  );
}
