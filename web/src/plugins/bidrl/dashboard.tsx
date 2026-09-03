/** The plugin's tile and detail on the dashboard. */
import {
  Badge,
  Countdown,
  Hint,
  PluginDisabledError,
  Row,
  Stack,
} from "@cc/ui";
import type { PluginSurfaceProps } from "@cc/ui";
import {
  cents,
  comparableHint,
} from "./model";
import { useFeed } from "./data";
import { LotBrowser } from "./lots";

export function Tile({ enabled }: PluginSurfaceProps) {
  const snap = useFeed("deals", "");
  if (snap.status === "loading") return <Hint>Loading…</Hint>;
  if (snap.status === "error") {
    return <Hint>{snap.error instanceof PluginDisabledError ? "Disabled." : snap.error.message}</Hint>;
  }
  const best = snap.data.lots[0];
  if (!best) {
    return <Hint>{enabled ? "No priced lots yet." : "Disabled."}</Hint>;
  }
  return (
    <Stack>
      <Row>
        <Badge tone="ok">{best.title}</Badge>
        {best.dealScore != null ? (
          <span className="cc-hint">{Math.round(best.dealScore * 100)}% under comparable</span>
        ) : null}
      </Row>
      <Hint>
        {cents(best.currentBidCents)} bid
        {best.priceCents != null ? ` · ${cents(best.priceCents)} ${comparableHint(best)}` : ""}
        {best.endsAt ? (
          <>
            {" · "}
            <Countdown iso={best.endsAt} />
          </>
        ) : null}
      </Hint>
    </Stack>
  );
}

export function Detail({ enabled }: PluginSurfaceProps) {
  const snap = useFeed("deals", "");
  if (snap.status !== "ready") return <Hint>Loading…</Hint>;
  if (snap.data.lots.length === 0) {
    return <Hint>{enabled ? "No priced lots yet." : "Disabled."}</Hint>;
  }
  return <LotBrowser lots={snap.data.lots.slice(0, 8)} empty="No priced lots yet." view="table" />;
}
