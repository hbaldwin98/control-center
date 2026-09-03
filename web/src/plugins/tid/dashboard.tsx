/** The plugin's tile and detail on the dashboard. */
import {
  Badge,
  Hint,
  PluginDisabledError,
  RelativeTime,
  Sparkline,
  Stack,
} from "@cc/ui";
import type { PluginSurfaceProps } from "@cc/ui";
import { kwh } from "./model";
import { useSummary } from "./data";
import { Metrics } from "./metrics";

export function Tile({ enabled }: PluginSurfaceProps) {
  const snap = useSummary();
  if (snap.status === "loading") return <Hint>Loading…</Hint>;
  if (snap.status === "error") {
    return (
      <Hint>{snap.error instanceof PluginDisabledError ? "Disabled." : snap.error.message}</Hint>
    );
  }
  const { monthKwh, lastSync, insight, spark } = snap.data;
  if (!lastSync && snap.data.days.length === 0) {
    return <Hint>{enabled ? "No readings yet." : "Disabled, and no readings recorded."}</Hint>;
  }
  return (
    <Stack>
      <div className="cc-row">
        <Badge tone={enabled ? "ok" : "neutral"}>{kwh(monthKwh)} kWh this month</Badge>
        {lastSync ? (
          <span className="cc-hint">
            <RelativeTime at={lastSync.at} prefix="synced" />
          </span>
        ) : null}
      </div>
      {spark.length > 0 ? <Sparkline values={spark} label="Daily kWh" /> : null}
      {insight ? <Hint>{insight.summary}</Hint> : null}
    </Stack>
  );
}

export function Detail({ enabled }: PluginSurfaceProps) {
  const snap = useSummary();
  if (snap.status !== "ready") return <Hint>Loading…</Hint>;
  if (snap.data.days.length === 0) {
    return <Hint>{enabled ? "No readings yet." : "Disabled."}</Hint>;
  }
  return (
    <Stack>
      <Metrics data={snap.data} />
      {snap.data.insight ? <Hint>{snap.data.insight.summary}</Hint> : null}
    </Stack>
  );
}
