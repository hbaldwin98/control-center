import type { Verdict } from "./types";

/** Labels a tile shows for `verdictOf`. */
export const VERDICTS: Record<Verdict, { tone: "ok" | "warn" | "danger"; label: string }> = {
  accounting: { tone: "danger", label: "accounting failed" },
  disabled: { tone: "danger", label: "disabled" },
  degraded: { tone: "warn", label: "degraded" },
  failing: { tone: "warn", label: "last job failed" },
  running: { tone: "ok", label: "running" },
  ok: { tone: "ok", label: "healthy" },
};
