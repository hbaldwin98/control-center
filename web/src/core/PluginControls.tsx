/**
 * The administrative controls for one plugin: state badges, problems, spend, budgets, and
 * the kill switch.
 *
 * They live here rather than inside one screen because a plugin's settings tab and any
 * other host surface that needs the kill switch must offer the same controls. Each piece
 * takes `heading`, so a screen that has already titled a card does not print the label twice.
 */
import { useState } from "react";
import {
  ApiError,
  Badge,
  Button,
  Callout,
  Field,
  Hint,
  Input,
  Meter,
  Money,
  Row,
  Select,
  Time,
  api,
  formatUSD,
} from "@cc/ui";
import type { Budget, ExceedAction, PluginState } from "./types";

/** State, automation, and runtime health, worst first. */
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
        <Hint>
          {state.disabledReason || "Disabled."}
          {state.disabledBy ? ` · ${state.disabledBy}` : ""}
          {state.disabledAt ? (
            <>
              {" · "}
              <Time iso={state.disabledAt} />
            </>
          ) : null}
        </Hint>
      ) : null}

      {state.accountingFailed ? (
        <Callout tone="danger">
          Settlement exceeded the reserved maximum at <Time iso={state.accountingFailed} />. Re-enable
          after the AI route is healthy again.
        </Callout>
      ) : null}

      {needsDailyBudget ? (
        <Callout tone="warn">
          An automated plugin needs a finite daily budget before it can be enabled.
        </Callout>
      ) : null}
    </>
  );
}

const WINDOWS = [
  { label: "Hour", reserved: "reservedHour", committed: "committedHour", limit: "hourly" },
  { label: "Day", reserved: "reservedDay", committed: "committedDay", limit: "daily" },
  { label: "Month", reserved: "reservedMonth", committed: "committedMonth", limit: "monthly" },
] as const;

/**
 * Committed and reserved spend for each budget window.
 *
 * A window with a limit gets a bar, because "$4.10 of $5.00" is a fact an operator has to
 * do arithmetic on and a bar is not. Reserved spend is drawn hatched: it is a hold that
 * may yet be released, and reading it as spent would overstate the day.
 */
export function SpendWindows({ state }: { state: PluginState }) {
  return (
    <div className="cc-windows">
      {WINDOWS.map((w) => {
        const committed = state[w.committed];
        const reserved = state[w.reserved];
        const limit = state.budget[w.limit];
        return (
          <div key={w.label} className="cc-windows__row">
            <div className="cc-windows__label">{w.label}</div>
            <div>
              <div className="cc-windows__value">
                <Money microUsd={committed} />
                <span className="cc-hint">
                  {reserved > 0 ? ` + ${formatUSD(reserved)} held` : ""}
                  {limit === 0 ? " / unlimited" : ` / ${formatUSD(limit)}`}
                </span>
              </div>
              <Meter
                value={committed}
                soft={reserved}
                max={limit}
                label={`${state.pluginId} ${w.label.toLowerCase()} budget`}
              />
            </div>
          </div>
        );
      })}
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
  heading = true,
  onChanged,
  onError,
}: {
  state: PluginState;
  heading?: boolean;
  onChanged: () => void;
  onError: (message: string | null) => void;
}) {
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const needsDailyBudget = state.automated && state.budget.daily === 0;
  const path = `/api/admin/plugins/${encodeURIComponent(state.pluginId)}`;

  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    onError(null);
    try {
      await fn();
      onChanged();
    } catch (err) {
      onError(messageOf(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      {heading ? <div className="cc-group__title">Kill switch</div> : null}
      <Hint>
        Disable rejects new jobs, AI calls, event handlers, plugin HTTP, publications, and storage
        writes, and closes this plugin&rsquo;s browser sessions. In-process code that ignores
        cancellation is not killed.
      </Hint>
      <Row>
        {state.enabled ? (
          <>
            <Input
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              placeholder="Reason (optional)"
              aria-label="Disable reason"
              className="cc-input--reason"
            />
            <Button
              type="button"
              variant="danger"
              disabled={busy}
              onClick={() => void act(() => api.post(`${path}/disable`, { reason: reason.trim() }))}
            >
              Disable
            </Button>
          </>
        ) : (
          <Button
            type="button"
            variant="primary"
            disabled={busy || needsDailyBudget}
            onClick={() => void act(() => api.post(`${path}/enable`))}
          >
            Enable
          </Button>
        )}
      </Row>
    </>
  );
}

export function BudgetForm({
  pluginId,
  budget,
  disabled,
  heading = true,
  onSaved,
  onError,
}: {
  pluginId: string;
  budget: Budget;
  disabled?: boolean;
  heading?: boolean;
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

  const off = disabled || busy;

  return (
    <form
      className="cc-stack"
      onSubmit={(e) => {
        e.preventDefault();
        void save();
      }}
    >
      {heading ? <div className="cc-group__title">Budget</div> : null}
      <div className="cc-toolbar">
        <Field label="Hourly $" hint="Empty is unlimited">
          <Input
            inputMode="decimal"
            value={hourly}
            onChange={(e) => setHourly(e.target.value)}
            placeholder="unlimited"
            aria-label="Hourly budget in dollars"
            disabled={off}
          />
        </Field>
        <Field label="Daily $" hint="Required for automated plugins">
          <Input
            inputMode="decimal"
            value={daily}
            onChange={(e) => setDaily(e.target.value)}
            placeholder="unlimited"
            aria-label="Daily budget in dollars"
            disabled={off}
          />
        </Field>
        <Field label="Monthly $" hint="Empty is unlimited">
          <Input
            inputMode="decimal"
            value={monthly}
            onChange={(e) => setMonthly(e.target.value)}
            placeholder="unlimited"
            aria-label="Monthly budget in dollars"
            disabled={off}
          />
        </Field>
        <Field label="On exceed" hint="When a window is spent">
          <Select
            value={onExceed}
            onChange={(e) => setOnExceed(e.target.value as ExceedAction)}
            disabled={off}
            aria-label="Action when a budget is exceeded"
          >
            <option value="reject">Reject the call</option>
            <option value="disable">Disable the plugin</option>
          </Select>
        </Field>
      </div>
      <Row>
        <Button type="submit" disabled={off}>
          {busy ? "Saving…" : "Save budget"}
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
