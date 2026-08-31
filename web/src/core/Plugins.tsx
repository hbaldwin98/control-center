/**
 * Plugin administration: the kill switch, budgets, health, and schema-backed config.
 *
 * This is the flat list — every plugin, every control, on one page. A single plugin's
 * detail screen at `/plugins/<id>` offers the same controls beside its live view, from the
 * same components.
 */
import { useCallback, useState } from "react";
import { Link } from "react-router-dom";
import {
  Async,
  Callout,
  Card,
  Hint,
  Page,
  PageHeader,
  Stack,
  api,
  useSnapshot,
} from "@cc/ui";
import { ConfigForm } from "./ConfigForm";
import {
  BudgetForm,
  KillSwitch,
  PluginBadges,
  PluginProblems,
  SpendWindows,
  budgetKey,
} from "./PluginControls";
import type { PluginState } from "./types";

export function Plugins() {
  const load = useCallback(
    (signal: AbortSignal) => api.snapshot<PluginState[]>("/api/admin/plugins", { signal }),
    [],
  );
  const plugins = useSnapshot<PluginState[]>(load, { events: ["core.plugin.**", "core.ai.usage"] });

  return (
    <Page>
      <PageHeader
        title="Plugins"
        lede="The kill switch, budgets, health, and schema-backed config. Disabled plugins stay listed with the reason."
      />
      <Async state={plugins} loading="Loading plugins…" empty="No plugins are registered yet.">
        {(list) => (
          <Stack>
            {list.map((st) => (
              <PluginCard key={st.pluginId} state={st} onChanged={plugins.reload} />
            ))}
          </Stack>
        )}
      </Async>
    </Page>
  );
}

function PluginCard({ state, onChanged }: { state: PluginState; onChanged: () => void }) {
  const [error, setError] = useState<string | null>(null);

  return (
    <Card
      title={
        <Link className="cc-tile__link" to={`/plugins/${encodeURIComponent(state.pluginId)}`}>
          {state.name || state.pluginId}
        </Link>
      }
      muted={!state.enabled}
      actions={<PluginBadges state={state} />}
    >
      <Stack>
        <Hint>
          <code>{state.pluginId}</code>
          {state.description ? ` — ${state.description}` : ""}
        </Hint>

        <PluginProblems state={state} />
        {error ? <Callout tone="danger">{error}</Callout> : null}

        <SpendWindows state={state} />

        <BudgetForm
          key={budgetKey(state)}
          pluginId={state.pluginId}
          budget={state.budget}
          onSaved={onChanged}
          onError={setError}
        />

        <ConfigForm
          key={`${state.pluginId}:${JSON.stringify(state.config ?? null)}`}
          pluginId={state.pluginId}
          schema={state.configSchema}
          value={state.config}
          onSaved={onChanged}
          onError={setError}
        />

        <KillSwitch state={state} onChanged={onChanged} onError={setError} />
      </Stack>
    </Card>
  );
}
