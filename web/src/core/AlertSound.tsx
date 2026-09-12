/** The alert chime: the listener that rings it, and the one control that governs it. */
import { useEffect, useState } from "react";
import { Button, Card, Checkbox, Hint, Row, Stack, stream } from "@cc/ui";
import { isMuted, ring, setMuted } from "./chime";

/**
 * Rings the chime whenever a notification becomes available.
 *
 * It listens to the lifecycle event rather than to any one plugin's alerts, so
 * everything that reaches the inbox is audible under exactly one rule and the shell
 * never learns what a plugin's events mean. Mounted once, in the authenticated frame.
 */
export function useAlertChime(): void {
  useEffect(() => stream.subscribe("core.notification.ready", () => ring()), []);
}

/** Mute, and a way to hear what the ping actually sounds like. */
export function AlertSoundCard() {
  const [muted, setMutedState] = useState(isMuted);

  return (
    <Card title="Alert sound">
      <Stack>
        <Hint>
          A short chime plays in this browser whenever something reaches the inbox. The setting
          is per-browser, not per-account, and it does not affect push or ntfy delivery.
        </Hint>
        <Checkbox
          label="Mute the alert chime"
          checked={muted}
          onChange={(e) => {
            setMuted(e.target.checked);
            setMutedState(e.target.checked);
          }}
        />
        <Row>
          {/* Also the gesture that lets the browser start audio at all: until something
              in the page is clicked, a chime is silently discarded. */}
          <Button type="button" onClick={() => ring(true)}>
            Test sound
          </Button>
        </Row>
        <Hint>
          If Test is silent, this browser is blocking sound for the site — allow audio for it and
          try again.
        </Hint>
      </Stack>
    </Card>
  );
}
