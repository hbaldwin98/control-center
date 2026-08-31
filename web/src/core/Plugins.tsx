/**
 * Plugin administration: the kill switch, budgets, health, and schema-backed config.
 *
 * This is the flat list — every plugin, every control, on one page. A single plugin's
 * detail screen at `/plugins/<id>` offers the same controls beside its live view.
 */
import { useCallback, useState } from "react";
import { Link } from "react-router-dom";
import {
  Callout,
  Card,
  EmptyState,
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

      {plugins.status === "error" ? (
        <Callout tone="danger">{plugins.error.message}</Callout>
      ) : null}

      {plugins.status === "loading" ? (
        <EmptyState>Loading…</EmptyState>
      ) : plugins.status === "ready" && plugins.data.length === 0 ? (
        <EmptyState>No plugins are registered yet.</EmptyState>
      ) : plugins.status === "ready" ? (
        <Stack>
          {plugins.data.map((st) => (
            <PluginCard key={st.pluginId} state={st} onChanged={plugins.reload} />
          ))}
        </Stack>
      ) : null}
    </Page>
  );
}

function PluginCard({ state, onChanged }: { state: PluginState; onChanged: () => void }) {
  const [error, setError] = useState<string | null>(null);

  return (
    <Card
      title={
        <Link to={`/plugins/${encodeURIComponent(state.pluginId)}`}>
          {state.name || state.pluginId}
        </Link>
      }
      actions={<PluginBadges state={state} />}
      muted={!state.enabled}
    >
      <Stack>
        <div className="cc-row cc-row--tight">
          <code className="cc-field__hint">{state.pluginId}</code>
        </div>

        {state.description ? <p className="cc-field__hint">{state.description}</p> : null}

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

        <p className="cc-field__hint">
          Disable rejects new jobs, AI calls, event handlers, plugin HTTP, publications, and
          storage writes, and closes this plugin's browser sessions. In-process code that
          ignores cancellation is not killed.
        </p>

        <KillSwitch state={state} onChanged={onChanged} onError={setError} />
      </Stack>
    </Card>
  );
}
