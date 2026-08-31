import { useCallback, useState } from "react";
import {
  ApiError,
  Badge,
  Button,
  Callout,
  Card,
  EmptyState,
  Field,
  Input,
  Page,
  PageHeader,
  Row,
  Stack,
  api,
  useSnapshot,
} from "@cc/ui";
import { ConfigForm } from "./ConfigForm";

type ExceedAction = "reject" | "disable";

type Budget = {
  hourly: number;
  daily: number;
  monthly: number;
  onExceed: ExceedAction;
};

type PluginState = {
  pluginId: string;
  enabled: boolean;
  automated: boolean;
  disabledAt: string | null;
  disabledBy: string;
  disabledReason: string;
  budget: Budget;
  reservedHour: number;
  committedHour: number;
  reservedDay: number;
  committedDay: number;
  reservedMonth: number;
  committedMonth: number;
  accountingFailed: string | null;
  name?: string;
  description?: string;
  health?: { desiredEnabled: boolean; runtime: string; lastError: string; errorKind?: string };
  config?: Record<string, unknown>;
  configSchema?: unknown;
};

/** Enable/disable, budgets, health, and schema-backed config. The kill switch lives here. */
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
  const [busy, setBusy] = useState(false);
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | null>(null);

  const needsDailyBudget = state.automated && state.budget.daily === 0;
  const canEnable = !state.enabled && !needsDailyBudget;

  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
      onChanged();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title={state.name || state.pluginId} muted={!state.enabled}>
      <Stack>
      <Row>
        {state.accountingFailed ? (
          <Badge tone="danger">accounting failed</Badge>
        ) : state.enabled ? (
          <Badge tone="ok">enabled</Badge>
        ) : (
          <Badge tone="danger">disabled</Badge>
        )}
        {state.automated ? <Badge>automated</Badge> : null}
        {state.health?.runtime === "degraded" ? (
          <Badge tone="warn">degraded{state.health.errorKind ? ` · ${state.health.errorKind}` : ""}</Badge>
        ) : null}
        <span className="cc-field__hint">
          <code>{state.pluginId}</code>
        </span>
      </Row>

        {state.description ? <p className="cc-field__hint">{state.description}</p> : null}

        {state.health?.lastError ? (
          <Callout tone="danger">{state.health.lastError}</Callout>
        ) : null}

      {!state.enabled ? (
        <div className="cc-field__hint">
          {state.disabledReason || "disabled"}
          {state.disabledBy ? ` · ${state.disabledBy}` : ""}
          {state.disabledAt ? ` · ${formatWhen(state.disabledAt)}` : ""}
        </div>
      ) : null}

      {state.accountingFailed ? (
        <Callout tone="danger">
          Settlement exceeded the reserved maximum at {formatWhen(state.accountingFailed)}.
          Re-enable after the AI route is healthy again.
        </Callout>
      ) : null}

      {needsDailyBudget ? (
        <Callout>An automated plugin needs a finite daily budget before it can be enabled.</Callout>
      ) : null}

      {error ? <Callout tone="danger">{error}</Callout> : null}

      <dl className="cc-stats">
        <dt>Hour</dt>
        <dd>{formatWindow(state.reservedHour, state.committedHour, state.budget.hourly)}</dd>
        <dt>Day</dt>
        <dd>{formatWindow(state.reservedDay, state.committedDay, state.budget.daily)}</dd>
        <dt>Month</dt>
        <dd>{formatWindow(state.reservedMonth, state.committedMonth, state.budget.monthly)}</dd>
      </dl>

      <BudgetForm
        key={`${state.pluginId}:${state.budget.hourly}:${state.budget.daily}:${state.budget.monthly}:${state.budget.onExceed}`}
        pluginId={state.pluginId}
        budget={state.budget}
        disabled={busy}
        onSaved={onChanged}
        onError={setError}
      />

      <ConfigForm
        key={`${state.pluginId}:${JSON.stringify(state.config ?? null)}`}
        pluginId={state.pluginId}
        schema={state.configSchema}
        value={state.config}
        disabled={busy}
        onSaved={onChanged}
        onError={setError}
      />

      <p className="cc-field__hint">
        Disable rejects new jobs, AI calls, event handlers, plugin HTTP, publications, and
        storage writes, and closes this plugin's browser sessions. In-process code that
        ignores cancellation is not killed.
      </p>

      <Row>
        {state.enabled ? (
          <>
            <Input
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              placeholder="Reason (optional)"
              aria-label="Disable reason"
              style={{ maxWidth: 280 }}
            />
            <Button
              type="button"
              variant="danger"
              disabled={busy}
              onClick={() =>
                void act(() =>
                  api.post(`/api/admin/plugins/${encodeURIComponent(state.pluginId)}/disable`, {
                    reason: reason.trim(),
                  }),
                )
              }
            >
              Disable
            </Button>
          </>
        ) : (
          <Button
            type="button"
            variant="primary"
            disabled={busy || !canEnable}
            onClick={() =>
              void act(() => api.post(`/api/admin/plugins/${encodeURIComponent(state.pluginId)}/enable`))
            }
          >
            Enable
          </Button>
        )}
      </Row>
      </Stack>
    </Card>
  );
}

function BudgetForm({
  pluginId,
  budget,
  disabled,
  onSaved,
  onError,
}: {
  pluginId: string;
  budget: Budget;
  disabled: boolean;
  onSaved: () => void;
  onError: (message: string | null) => void;
}) {
  const [hourly, setHourly] = useState(microToInput(budget.hourly));
  const [daily, setDaily] = useState(microToInput(budget.daily));
  const [monthly, setMonthly] = useState(microToInput(budget.monthly));
  const [onExceed, setOnExceed] = useState<ExceedAction>(budget.onExceed || "reject");
  const [busy, setBusy] = useState(false);

  const save = async () => {
    setBusy(true);
    onError(null);
    try {
      const body: Budget = {
        hourly: inputToMicro(hourly),
        daily: inputToMicro(daily),
        monthly: inputToMicro(monthly),
        onExceed,
      };
      await api.put(`/api/admin/plugins/${encodeURIComponent(pluginId)}/budget`, body);
      onSaved();
    } catch (err) {
      onError(err instanceof ApiError ? err.message : err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form
      className="cc-stack"
      onSubmit={(e) => {
        e.preventDefault();
        void save();
      }}
    >
      <Row>
        <Field label="Hourly $" hint="Empty is unlimited">
          <Input
            inputMode="decimal"
            value={hourly}
            onChange={(e) => setHourly(e.target.value)}
            placeholder="unlimited"
            aria-label="Hourly budget in dollars"
            disabled={disabled || busy}
          />
        </Field>
        <Field label="Daily $" hint="Required for automated plugins">
          <Input
            inputMode="decimal"
            value={daily}
            onChange={(e) => setDaily(e.target.value)}
            placeholder="unlimited"
            aria-label="Daily budget in dollars"
            disabled={disabled || busy}
          />
        </Field>
        <Field label="Monthly $">
          <Input
            inputMode="decimal"
            value={monthly}
            onChange={(e) => setMonthly(e.target.value)}
            placeholder="unlimited"
            aria-label="Monthly budget in dollars"
            disabled={disabled || busy}
          />
        </Field>
        <Field label="On exceed">
          <select
            className="cc-input"
            value={onExceed}
            onChange={(e) => setOnExceed(e.target.value as ExceedAction)}
            disabled={disabled || busy}
            aria-label="Action when a budget is exceeded"
          >
            <option value="reject">Reject the call</option>
            <option value="disable">Disable the plugin</option>
          </select>
        </Field>
        <Button type="submit" disabled={disabled || busy}>
          Save budget
        </Button>
      </Row>
    </form>
  );
}

function formatWindow(reserved: number, committed: number, limit: number): string {
  const used = `${formatUSD(reserved)} reserved + ${formatUSD(committed)} committed`;
  return limit === 0 ? `${used} / unlimited` : `${used} / ${formatUSD(limit)}`;
}

function formatUSD(micro: number): string {
  const dollars = micro / 1_000_000;
  return dollars.toLocaleString(undefined, {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: 2,
    maximumFractionDigits: 6,
  });
}

function microToInput(micro: number): string {
  if (micro === 0) return "";
  const dollars = micro / 1_000_000;
  return String(Number(dollars.toFixed(6)));
}

function inputToMicro(raw: string): number {
  const s = raw.trim();
  if (s === "") return 0;
  const n = Number(s);
  if (!Number.isFinite(n) || n < 0) {
    throw new Error("budget amounts must be non-negative numbers");
  }
  return Math.round(n * 1_000_000);
}

function formatWhen(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}
