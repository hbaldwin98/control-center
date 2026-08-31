/**
 * Hello — the validating plugin's UI.
 *
 * It exercises the whole frontend contract: a full screen, a live dashboard tile, and a
 * detail panel, all from one `useTicks` hook that loads a snapshot and folds later events
 * into it. Imports `@cc/ui` and this directory only.
 */
import { useCallback, useState } from "react";
import {
  Badge,
  Button,
  Callout,
  Card,
  EmptyState,
  Page,
  PageHeader,
  PluginDisabledError,
  RelativeTime,
  Stack,
  pluginApi,
  useSnapshot,
} from "@cc/ui";
import type { PluginModule, PluginSurfaceProps, UseSnapshotResult } from "@cc/ui";

type Tick = {
  id: number;
  at: string;
  note: string;
  blobKey: string;
  aiText: string;
  eventId: number;
};

type TicksPage = {
  note: string;
  ticks: Tick[];
};

const api = pluginApi("hello");

/**
 * The plugin's one piece of state, live.
 *
 * `hello.ticked` carries the whole row, so it folds into the snapshot rather than
 * invalidating it — no refetch per tick, and the tile updates the instant the event lands.
 */
function useTicks(): UseSnapshotResult<TicksPage> {
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

function TickTable({ ticks, limit }: { ticks: Tick[]; limit?: number }) {
  const rows = limit ? ticks.slice(0, limit) : ticks;
  return (
    <table className="cc-table">
      <thead>
        <tr>
          <th>When</th>
          <th>Note</th>
          <th>AI</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((t) => (
          <tr key={t.id}>
            <td className="cc-mono">{t.at}</td>
            <td>{t.note}</td>
            <td className="cc-truncate">{t.aiText || "—"}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function History() {
  const snap = useTicks();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const tick = async () => {
    setBusy(true);
    setError(null);
    try {
      await api.post("/tick");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const disabled = snap.error instanceof PluginDisabledError;
  const ticks = snap.status === "ready" ? snap.data.ticks : [];
  const note = snap.status === "ready" ? snap.data.note : "";

  return (
    <Page>
      <PageHeader
        title="Hello"
        lede="The validating plugin. Each tick writes a row, a blob, an event, a tiny AI call, and opens a host-managed browser page."
        actions={
          <Button variant="primary" disabled={busy || disabled} onClick={() => void tick()}>
            Tick now
          </Button>
        }
      />
      <Stack>
        {error ? <Callout tone="danger">{error}</Callout> : null}
        {disabled ? (
          <Callout>
            Hello is disabled. Enable it on the <a href="/plugins">Plugins</a> screen to run ticks.
          </Callout>
        ) : null}
        {snap.status === "error" && !disabled ? (
          <Callout tone="danger">{snap.error.message}</Callout>
        ) : null}
        {note ? <div className="cc-field__hint">Config note: {note}</div> : null}

        {snap.status === "loading" ? (
          <EmptyState>Loading…</EmptyState>
        ) : ticks.length === 0 ? (
          <EmptyState>
            No ticks yet. Enable the plugin and press Tick now, or wait for the minute cron.
          </EmptyState>
        ) : (
          <Card title="History">
            <TickTable ticks={ticks} />
          </Card>
        )}
      </Stack>
    </Page>
  );
}

/** The dashboard tile: the last tick, and what the model said about it. */
function Tile({ enabled }: PluginSurfaceProps) {
  const snap = useTicks();

  if (snap.status === "loading") return <div className="cc-field__hint">Loading…</div>;
  if (snap.status === "error") {
    return (
      <div className="cc-field__hint">
        {snap.error instanceof PluginDisabledError ? "Disabled." : snap.error.message}
      </div>
    );
  }

  const [latest] = snap.data.ticks;
  if (!latest) {
    return (
      <div className="cc-field__hint">
        {enabled ? "No ticks yet." : "Disabled, and no ticks recorded."}
      </div>
    );
  }

  return (
    <div className="cc-stack">
      <div className="cc-row cc-row--tight">
        <Badge tone={enabled ? "ok" : "neutral"}>{snap.data.ticks.length} ticks</Badge>
        <span className="cc-field__hint">
          <RelativeTime at={latest.at} prefix="last" />
        </span>
      </div>
      {latest.aiText ? <div className="cc-truncate">{latest.aiText}</div> : null}
    </div>
  );
}

/** The panel on Hello's detail screen: the most recent ticks, without leaving the page. */
function Detail() {
  const snap = useTicks();
  if (snap.status !== "ready") return <div className="cc-field__hint">Loading…</div>;
  if (snap.data.ticks.length === 0) return <div className="cc-field__hint">No ticks yet.</div>;
  return <TickTable ticks={snap.data.ticks} limit={8} />;
}

const hello: PluginModule = {
  id: "hello",
  nav: [{ path: "/hello", label: "Hello" }],
  routes: [{ path: "/hello", element: <History /> }],
  dashboard: {
    summary: "Ticks a row, a blob, an event, and a tiny AI call every minute.",
    live: ["hello.ticked"],
    tile: Tile,
    detail: Detail,
  },
};

export default hello;
