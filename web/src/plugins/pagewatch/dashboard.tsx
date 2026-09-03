/** The plugin's tile and detail on the dashboard. */
import {
  Badge,
  Hint,
  PluginDisabledError,
  RelativeTime,
  Row,
  Stack,
} from "@cc/ui";
import type { PluginSurfaceProps } from "@cc/ui";
import {
  statusLabel,
  statusTone,
  type Check,
} from "./model";
import { useChecks } from "./data";
import { CheckTable } from "./checks";

export function tileErrorMessage(error: Error): string {
  return error instanceof PluginDisabledError ? "Disabled." : error.message;
}

export function emptyTileMessage(enabled: boolean): string {
  return enabled ? "No checks yet." : "Disabled, and no checks recorded.";
}

export function ReadyTile({ latest, enabled }: { latest: Check; enabled: boolean }) {
  return (
    <Stack>
      <Row>
        <Badge tone={enabled ? statusTone(latest.status) : "neutral"}>{statusLabel(latest.status)}</Badge>
        <span className="cc-hint"><RelativeTime at={latest.checkedAt} prefix="checked" /></span>
      </Row>
      <Hint>{latest.summary}</Hint>
    </Stack>
  );
}

export function Tile({ enabled }: PluginSurfaceProps) {
  const snap = useChecks();
  if (snap.status === "loading") return <Hint>Loading…</Hint>;
  if (snap.status === "error") return <Hint>{tileErrorMessage(snap.error)}</Hint>;
  const latest = snap.data.checks[0];
  if (!latest) return <Hint>{emptyTileMessage(enabled)}</Hint>;
  return <ReadyTile latest={latest} enabled={enabled} />;
}

export function Detail() {
  const snap = useChecks();
  if (snap.status !== "ready") return <Hint>Loading…</Hint>;
  if (snap.data.checks.length === 0) return <Hint>No checks yet.</Hint>;
  return <CheckTable checks={snap.data.checks} limit={8} />;
}
