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
  events?: EventSpec[];
};

export type EventField = {
  name: string;
  type: string;
  purpose: string;
  path: string;
};

/** One published event a plugin declared. Type and match are already prefixed. */
export type EventSpec = {
  type: string;
  match: string;
  purpose: string;
  fields: EventField[];
};

export type CatalogEvent = EventSpec & {
  source: string;
  name: string;
};

export type EnvelopeField = {
  path: string;
  type: string;
  purpose: string;
};

/** `/api/admin/notifications/catalog` — what a rule can match and interpolate. */
export type EventCatalog = {
  envelope: EnvelopeField[];
  events: CatalogEvent[];
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

/**
 * Open and current-failure counts for one plugin's jobs.
 *
 * `jobs` must be newest first, which is the order `/api/jobs` returns. Historical
 * failures do not count: a plugin that failed last week and is running now is running.
 */
export type JobPulse = {
  running: number;
  waiting: number;
  failing: boolean;
};

export const idlePulse: JobPulse = { running: 0, waiting: 0, failing: false };

/** Group a newest-first job list, preserving that order inside each plugin. */
export function jobsByPlugin(jobs: Job[]): Map<string, Job[]> {
  const byPlugin = new Map<string, Job[]>();
  for (const job of jobs) {
    const list = byPlugin.get(job.pluginId);
    if (list) list.push(job);
    else byPlugin.set(job.pluginId, [job]);
  }
  return byPlugin;
}

export function pulseOf(jobs: Job[]): JobPulse {
  let running = 0;
  let waiting = 0;
  for (const job of jobs) {
    if (job.state === "running") running += 1;
    else if (job.state === "pending" || job.state === "retry_wait" || job.state === "cancel_requested") {
      waiting += 1;
    }
  }
  const latest = jobs[0];
  const failing =
    latest !== undefined &&
    (latest.state === "failed" || latest.state === "dead" || latest.state === "retry_wait");
  return { running, waiting, failing };
}

/** The one-word health verdict a tile shows, worst first. */
export type Verdict = "accounting" | "disabled" | "degraded" | "failing" | "running" | "ok";

export function verdictOf(state: PluginState, pulse: JobPulse = idlePulse): Verdict {
  if (state.accountingFailed) return "accounting";
  if (!state.enabled) return "disabled";
  if (state.health?.runtime === "degraded") return "degraded";
  if (pulse.running > 0) return "running";
  if (pulse.failing) return "failing";
  return "ok";
}

/** Verdicts an operator should look at, as opposed to healthy or currently running. */
export function needsAttention(verdict: Verdict): boolean {
  return verdict !== "ok" && verdict !== "running";
}
