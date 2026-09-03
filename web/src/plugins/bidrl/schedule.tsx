/** What the schedule is doing, for the screen that is not the schedule screen. */
import {
  useState,
} from "react";
import {
  Badge,
  Button,
  Callout,
  Card,
  Hint,
  Link,
  Stack,
} from "@cc/ui";
import {
  automationSummary,
} from "./model";
import { api } from "./api";
import { useAutomation } from "./data";
import { savedOn } from "./lotparts";

/**
 * What the schedule is doing, on the page you open first. A latch is the one thing
 * here that needs an answer, so it is the only state that offers a button.
 */
export function AutomationStrip() {
  const snap = useAutomation();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  if (snap.status !== "ready") return null;
  const a = snap.data.automation;

  const resume = async () => {
    setBusy(true);
    setError(null);
    try {
      await api.post("/automation/resume");
      snap.reload();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not resume.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card
      title="Automation"
      actions={
        <>
          {a.newFindings > 0 ? <Link to="/bidrl/findings">{a.newFindings} to review</Link> : null}
          <Link to="/bidrl/automation">Automation</Link>
        </>
      }
    >
      <Stack>
        {a.throttled ? (
          <Callout tone="warn">
            BidRL refused repeated requests, so scheduled collection stopped and will not
            start again on its own. Resume it once you are satisfied nothing is wrong.
          </Callout>
        ) : null}
        <div className="bidrl-automation">
          <Badge tone={a.throttled ? "warn" : a.enabled ? "ok" : "neutral"}>
            {automationSummary(a)}
          </Badge>
          {a.lastSweepAt ? (
            <Hint>
              Last sweep {savedOn(a.lastSweepAt)}
              {a.lastSweepNote ? ` — ${a.lastSweepNote}` : ""}
            </Hint>
          ) : null}
          {a.lastMatchAt ? (
            <Hint>
              Last match {savedOn(a.lastMatchAt)}
              {a.lastMatchNote ? ` — ${a.lastMatchNote}` : ""}
            </Hint>
          ) : null}
        </div>
        {error ? <Callout tone="danger">{error}</Callout> : null}
        {a.throttled ? (
          <div className="bidrl-actions">
            <Button variant="primary" disabled={busy} onClick={() => void resume()}>
              Resume now
            </Button>
          </div>
        ) : null}
      </Stack>
    </Card>
  );
}
