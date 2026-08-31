/**
 * The administrative controls for one plugin: state badges, the kill switch, budgets, and
 * spend.
 *
 * They live here rather than inside one screen because the Plugins list and a single
 * plugin's detail screen must offer exactly the same controls — an operator who drilled
 * into a plugin should not have to navigate back to turn it off.
 */
import { useState } from "react";
import {
  ApiError,
  Badge,
  Button,
  Callout,
  Field,
  Input,
  Meter,
  Row,
  api,
  formatUSD,
  formatWhen,
} from "@cc/ui";
import type { Budget, ExceedAction, PluginState } from "./types";

/** State, automation, and runtime health, in worst-first order. */
export function PluginBadges({ state }: { state: PluginState }) {
  return (
    <>
      {state.accountingFailed ? (
        <Badge tone="danger">accounting failed</Badge>
      ) : state.enabled ? (
        <Badge tone="ok">enabled</Badge>
      ) : (
        <Badge tone="danger">disabled</Badge>
      )}
      {state.automated ? <Badge>automated</Badge> : null}
      {state.health?.runtime === "degraded" ? (
        <Badge tone="warn">
          degraded{state.health.errorKind ? ` · ${state.health.errorKind}` : ""}
        </Badge>
      ) : null}
    </>
  );
}

/** Everything the host wants said about a plugin that is not currently healthy. */
export function PluginProblems({ state }: { state: PluginState }) {
  const needsDailyBudget = state.automated && state.budget.daily === 0;
  return (
    <>
      {state.health?.lastError ? <Callout tone="danger">{state.health.lastError}</Callout> : null}
      {!state.enabled ? (
        <div className="cc-field__hint">
          {state.disabledReason || "disabled"}
          {state.disabledBy ? ` · ${state.disabledBy}` : ""}
          {state.disabledAt ? ` · ${formatWhen(state.disabledAt)}` : ""}
        </div>
      ) : null}
      {state.accountingFailed ? (
        <Callout tone="danger">
          Settlement exceeded the reserved maximum at {formatWhen(state.accountingFailed)}. Re-enable
          after the AI route is healthy again.
        </Callout>
      ) : null}
      {needsDailyBudget ? (
        <Callout>An automated plugin needs a finite daily budget before it can be enabled.</Callout>
      ) : null}
    </>
  );
}

/** Reserved and committed spend for each budget window, with a bar where a limit is set. */
export function SpendWindows({ state }: { state: PluginState }) {
  const windows = [
    { label: "Hour", reserved: state.reservedHour, committed: state.committedHour, limit: state.budget.hourly },
    { label: "Day", reserved: state.reservedDay, committed: state.committedDay, limit: state.budget.daily },
    { label: "Month", reserved: state.reservedMonth, committed: state.committedMonth, limit: state.budget.monthly },
  ];
  return (
    <div className="cc-windows">
      {windows.map((w) => (
        <div key={w.label} className="cc-windows__row">
          <div className="cc-windows__label">{w.label}</div>
          <div>
            <div className="cc-windows__value">
              {formatUSD(w.committed)}
              <span className="cc-field__hint">
                {w.reserved > 0 ? ` + ${formatUSD(w.reserved)} held` : ""}
                {w.limit === 0 ? " / unlimited" : ` / ${formatUSD(w.limit)}`}
              </span>
            </div>
            <Meter
              value={w.committed}
              soft={w.reserved}
              max={w.limit}
              label={`${state.pluginId} ${w.label.toLowerCase()} budget`}
            />
          </div>
        </div>
      ))}
    </div>
  );
}

/**
 * The host-capability kill switch.
 *
 * Disable rejects new jobs, AI dispatches, event handlers, plugin HTTP, publications, and
 * storage mutations, closes the plugin's browser sessions, and cancels admitted contexts.
 * It does not claim to terminate in-process code that ignores cancellation.
 */
export function KillSwitch({
  state,
  busy,
  onChanged,
  onError,
}: {
  state: PluginState;
  busy?: boolean;
  onChanged: () => void;
  onError: (message: string | null) => void;
}) {
  const [reason, setReason] = useState("");
  const [working, setWorking] = useState(false);
  const disabledControls = busy || working;
  const needsDailyBudget = state.automated && state.budget.daily === 0;

  const act = async (fn: () => Promise<unknown>) => {
    setWorking(true);
    onError(null);
    try {
      await fn();
      onChanged();
    } catch (err) {
      onError(messageOf(err));
    } finally {
      setWorking(false);
    }
  };

  const path = `/api/admin/plugins/${encodeURIComponent(state.pluginId)}`;

  return (
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
            disabled={disabledControls}
            onClick={() => void act(() => api.post(`${path}/disable`, { reason: reason.trim() }))}
          >
            Disable
          </Button>
        </>
      ) : (
        <Button
          type="button"
          variant="primary"
          disabled={disabledControls || needsDailyBudget}
          onClick={() => void act(() => api.post(`${path}/enable`))}
        >
          Enable
        </Button>
      )}
    </Row>
  );
}

export function BudgetForm({
  pluginId,
  budget,
  disabled,
  onSaved,
  onError,
}: {
  pluginId: string;
  budget: Budget;
  disabled?: boolean;
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
      onError(messageOf(err));
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

/** A stable key for a form whose defaults come from server state. */
export function budgetKey(state: PluginState): string {
  const b = state.budget;
  return `${state.pluginId}:${b.hourly}:${b.daily}:${b.monthly}:${b.onExceed}`;
}

export function messageOf(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  return err instanceof Error ? err.message : String(err);
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
