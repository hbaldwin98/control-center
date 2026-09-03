/** The plugin's one screen: press a tick, then watch the history it writes. */
import { useState } from "react";
import { Button, Callout, Card, EmptyState, Hint, Loading, Page, PageHeader, PluginAIHint, PluginDisabledError, Stack } from "@cc/ui";
import { api } from "../api";
import { useTicks } from "../data";
import { TickTable } from "../ticks";

export function History() {
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
            Hello is disabled. Enable it on the <a href="/plugins/hello/settings">plugin screen</a> to run ticks.
          </Callout>
        ) : null}
        <PluginAIHint pluginId="hello" />
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
            <TickTable ticks={ticks} />
          </Card>
        )}
      </Stack>
    </Page>
  );
}
