/** Every read this plugin makes, as one hook per endpoint. */
import { useCallback } from "react";
import { useSnapshot } from "@cc/ui";
import type { UseSnapshotResult } from "@cc/ui";
import { applyCheckEvent, eventBoundary, type ChecksPage } from "./model";
import { api } from "./api";

export function useChecks(): UseSnapshotResult<ChecksPage> {
  const load = useCallback(async (signal: AbortSignal) => {
    const data = await api.get<ChecksPage>("/checks", signal);
    return { data, asOfEventId: eventBoundary(data.checks) };
  }, []);

  return useSnapshot(load, {
    events: "pagewatch.check.completed",
    apply: applyCheckEvent,
  });
}
