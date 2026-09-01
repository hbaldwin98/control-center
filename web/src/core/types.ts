/** Shapes the host's admin API returns, shared by the core screens that render them. */

export type ExceedAction = "reject" | "disable";

export type Budget = {
  hourly: number;
  daily: number;
  monthly: number;
  onExceed: ExceedAction;
};

export type PluginHealth = {
  desiredEnabled: boolean;
  runtime: string;
  lastError: string;
  errorKind?: string;
};

/** One row of `/api/admin/plugins`. Amounts are micro-USD integers. */
export type PluginState = {
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
  health?: PluginHealth;
  config?: Record<string, unknown>;
  configSchema?: unknown;
  models?: ModelNeed[];
};

export type ModelNeedStatus = "missing" | "ready" | "unhealthy" | "capability_mismatch";

/** One logical AI route a plugin declared, with whether a matching route exists. */
export type ModelNeed = {
  name: string;
  capabilities: string[];
  purpose: string;
  status: ModelNeedStatus;
  healthy: boolean;
  lastError?: string;
  provider?: string;
  model?: string;
};

export type JobState =
  | "pending"
  | "running"
  | "retry_wait"
  | "succeeded"
  | "failed"
  | "dead"
  | "cancel_requested"
  | "cancelled";

/** One row of `/api/jobs`. */
export type Job = {
  id: number;
  pluginId: string;
  name: string;
  state: JobState;
  attempt: number;
  maxAttempts: number;
  progress: number;
  progressMessage?: string;
  lastError?: string;
  startedAt?: string;
  finishedAt?: string;
  createdAt: string;
};

/** Work the host still owes: it is running, waiting to run, or waiting to retry. */
export function isOpen(state: JobState): boolean {
  return (
    state === "running" ||
    state === "pending" ||
    state === "retry_wait" ||
    state === "cancel_requested"
  );
}

/** Work that ended badly. `cancelled` is a decision, not a failure, so it is excluded. */
export function isFailure(state: JobState): boolean {
  return state === "failed" || state === "dead";
}

/** The one-word health verdict a tile shows, worst first. */
export type Verdict = "accounting" | "disabled" | "degraded" | "failing" | "ok";

export function verdictOf(state: PluginState, failures: number): Verdict {
  if (state.accountingFailed) return "accounting";
  if (!state.enabled) return "disabled";
  if (state.health?.runtime === "degraded") return "degraded";
  if (failures > 0) return "failing";
  return "ok";
}
