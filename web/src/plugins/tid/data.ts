/** Every read this plugin makes, as one hook per endpoint. */
import { useCallback } from "react";
import { useSnapshot } from "@cc/ui";
import type { UseSnapshotResult } from "@cc/ui";
import type { History, Summary } from "./model";
import { api } from "./api";

export function useSummary(): UseSnapshotResult<Summary> {
  const load = useCallback(async (signal: AbortSignal) => {
    const data = await api.get<Summary>("/summary", signal);
    return { data, asOfEventId: String(data.latestEventId ?? 0) };
  }, []);
  return useSnapshot<Summary>(load, { events: ["tid.synced", "tid.insight"] });
}

export function useHistory(): UseSnapshotResult<History> {
  const load = useCallback(async (signal: AbortSignal) => {
    const data = await api.get<History>("/history", signal);
    return { data, asOfEventId: String(data.latestEventId ?? 0) };
  }, []);
  return useSnapshot<History>(load, { events: ["tid.synced"] });
}
