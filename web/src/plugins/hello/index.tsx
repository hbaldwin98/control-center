/**
 * Hello — history of ticks from the validating plugin.
 *
 * Imports `@cc/ui` and this directory only.
 */
import { useCallback, useState } from "react";
import {
  Button,
  Callout,
  Card,
  Dash,
  EmptyState,
  Hint,
  Loading,
  Page,
  PageHeader,
  PluginDisabledError,
  Stack,
  Table,
  Time,
  pluginApi,
  useSnapshot,
} from "@cc/ui";
import type { PluginModule } from "@cc/ui";

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

function History() {
  const load = useCallback(async (signal: AbortSignal) => {
    const data = await api.get<TicksPage>("/ticks", signal);
    let latest = 0;
    for (const t of data.ticks) {
      if (t.eventId > latest) latest = t.eventId;
    }
    return { data, asOfEventId: String(latest) };
  }, []);
  const snap = useSnapshot<TicksPage>(load, {
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
        {note ? <Hint>Config note: {note}</Hint> : null}

        {snap.status === "loading" ? (
          <Loading label="Loading ticks…" />
        ) : ticks.length === 0 ? (
          <EmptyState>No ticks yet. Enable the plugin and press Tick now, or wait for the minute cron.</EmptyState>
        ) : (
          <Card title="History">
            <Table
              head={
                <>
                  <th>When</th>
                  <th>Note</th>
                  <th>AI</th>
                </>
              }
            >
              {ticks.map((t) => (
                <tr key={t.id}>
                  <td>
                    <Time iso={t.at} />
                  </td>
                  <td>{t.note}</td>
                  <td>{t.aiText || <Dash />}</td>
                </tr>
              ))}
            </Table>
          </Card>
        )}
      </Stack>
    </Page>
  );
}

const hello: PluginModule = {
  id: "hello",
  nav: [{ path: "/hello", label: "Hello" }],
  routes: [{ path: "/hello", element: <History /> }],
};

export default hello;
