/** The plugin's tile and detail on the dashboard. */
import { Badge, Hint, PluginDisabledError, RelativeTime, Stack } from "@cc/ui";
import type { PluginSurfaceProps } from "@cc/ui";
import { useTicks } from "./data";
import { TickTable } from "./ticks";

/** The dashboard tile: how many ticks, how long ago, and what the model said. */
export function Tile({ enabled }: PluginSurfaceProps) {
  const snap = useTicks();

  if (snap.status === "loading") return <Hint>Loading…</Hint>;
  if (snap.status === "error") {
    return (
      <Hint>{snap.error instanceof PluginDisabledError ? "Disabled." : snap.error.message}</Hint>
    );
  }

  const [latest] = snap.data.ticks;
  if (!latest) return <Hint>{enabled ? "No ticks yet." : "Disabled, and no ticks recorded."}</Hint>;

  return (
    <Stack>
      <div className="cc-row">
        <Badge tone={enabled ? "ok" : "neutral"}>{snap.data.ticks.length} ticks</Badge>
        <span className="cc-hint">
          <RelativeTime at={latest.at} prefix="last" />
        </span>
      </div>
      {latest.aiText ? <Hint>{latest.aiText}</Hint> : null}
    </Stack>
  );
}

/** The panel on Hello's detail screen: recent ticks, without leaving the page. */
export function Detail() {
  const snap = useTicks();
  if (snap.status !== "ready") return <Hint>Loading…</Hint>;
  if (snap.data.ticks.length === 0) return <Hint>No ticks yet.</Hint>;
  return <TickTable ticks={snap.data.ticks} limit={8} />;
}
