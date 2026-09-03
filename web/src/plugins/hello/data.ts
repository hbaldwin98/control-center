/** Every read this plugin makes, as one hook per endpoint. */
import { useCallback } from "react";
import { useSnapshot } from "@cc/ui";
import type { UseSnapshotResult } from "@cc/ui";
import type { Tick, TicksPage } from "./model";
import { api } from "./api";

/**
 * The plugin's one piece of state, live.
 *
 * `hello.ticked` carries the whole row, so it folds into the snapshot rather than
 * invalidating it — no refetch per tick, and the tile updates the instant the event lands.
 */
export function useTicks(): UseSnapshotResult<TicksPage> {
  const load = useCallback(async (signal: AbortSignal) => {
    const data = await api.get<TicksPage>("/ticks", signal);
    let latest = 0;
    for (const t of data.ticks) {
      if (t.eventId > latest) latest = t.eventId;
    }
    return { data, asOfEventId: String(latest) };
  }, []);

  return useSnapshot<TicksPage>(load, {
    events: "hello.ticked",
    apply: (page, event) => {
      const eventId = Number(event.id);
      if (!Number.isFinite(eventId) || page.ticks.some((t) => t.eventId === eventId)) {
        return page;
      }
      const payload = (event.payload ?? {}) as Partial<Tick>;
      return {
        note: payload.note ?? page.note,
        ticks: [
          {
            id: eventId,
            at: payload.at ?? event.createdAt,
            note: payload.note ?? "",
            blobKey: payload.blobKey ?? "",
            aiText: payload.aiText ?? "",
            eventId,
          },
          ...page.ticks,
        ],
      };
    },
  });
}
