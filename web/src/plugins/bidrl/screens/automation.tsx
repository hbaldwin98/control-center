import {
  useMemo,
  useState,
  type ReactNode,
} from "react";
import {
  Badge,
  Button,
  Callout,
  Card,
  Grid,
  Hint,
  Link,
  Loading,
  Metric,
  Page,
  PageHeader,
  PluginDisabledError,
  RelativeTime,
  Stack,
  Time,
} from "@cc/ui";
import {
  automationLocations,
  automationSummary,
  matchPlan,
  locationLabel,
  sweepPlan,
} from "../model";
import { api } from "../api";
import { useAutomation, useLocations } from "../data";
import { BidrlTabs, StatLink } from "../chrome";
import { Notices } from "../actions";

/**
 * The whole schedule on one screen: what each tick would do next, what it did last, and
 * the one control automation has — clearing a latch. Settings live in the plugin's own
 * configuration, which only the administration API writes, so this screen reports them
 * and links to where they are changed rather than pretending to own them.
 */
export function AutomationScreen() {
  const snap = useAutomation();
  const locations = useLocations();
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const disabled = snap.error instanceof PluginDisabledError;
  const a = snap.status === "ready" ? snap.data.automation : null;

  const labels = useMemo(() => {
    const map = new Map<string, string>();
    if (locations.status === "ready") {
      for (const loc of locations.data.locations) map.set(loc.id, locationLabel(loc));
    }
    return map;
  }, [locations]);

  const resume = async () => {
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      await api.post("/automation/resume");
      setNotice("Resumed. The next tick will collect again.");
      snap.reload();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not resume.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Page>
      <PageHeader
        eyebrow="BidRL / Actions"
        title="Automation"
        lede="Two ticks, six hours apart. The sweep collects new auctions at your locations; the match runs your watchlists over what is already collected and never touches BidRL."
      />
      <Stack>
        <BidrlTabs />
        <Notices message={notice} error={error} disabled={disabled} />
        {snap.status === "loading" ? <Loading label="Loading automation…" /> : null}
        {snap.status === "error" && !disabled ? <Callout tone="danger">{snap.error.message}</Callout> : null}
        {a ? (
          <>
            {a.throttled ? (
              <Card title="Stopped">
                <Stack>
                  <Callout tone="warn">
                    BidRL refused repeated requests, so scheduled collection stopped and will
                    not start again on its own. It stays stopped until you say otherwise
                    {a.throttledUntil ? (
                      <>
                        {" "}
                        — or until <Time iso={a.throttledUntil} /> if you leave it
                      </>
                    ) : null}
                    . The match tick keeps running: it never touches BidRL.
                  </Callout>
                  <div className="bidrl-actions">
                    <Button variant="primary" disabled={disabled || busy} onClick={() => void resume()}>
                      Resume collection
                    </Button>
                  </div>
                </Stack>
              </Card>
            ) : null}
            <Grid density="metric">
              <Metric
                label="Schedule"
                value={a.throttled ? "Stopped" : a.enabled ? "On" : "Off"}
                hint={automationSummary(a)}
                tone={a.throttled ? "warn" : a.enabled ? "ok" : "neutral"}
              />
              <Metric
                label="Locations"
                value={String(a.locations)}
                hint={a.locations === 0 ? "the sweep does nothing" : automationLocations(a, labels).join(", ")}
                tone={a.enabled && a.locations === 0 ? "warn" : "neutral"}
              />
              <Metric
                label="Queued auctions"
                value={String(a.queuedAuctions)}
                hint={`up to ${a.maxAuctionsPerSweep} a tick`}
              />
              <StatLink
                to="/bidrl/findings"
                label="To review"
                value={String(a.newFindings)}
                hint={a.newFindings > 0 ? "waiting on you" : "nothing waiting"}
                tone={a.newFindings > 0 ? "ok" : "neutral"}
              />
            </Grid>
            <TickCard
              title="Sweep"
              schedule={`${a.sweepSchedule} (${a.timeZone})`}
              plan={sweepPlan(a)}
              at={a.lastSweepAt}
              note={a.lastSweepNote}
            />
            <TickCard
              title="Match"
              schedule={`${a.matchSchedule} (${a.timeZone})`}
              plan={matchPlan(a)}
              at={a.lastMatchAt}
              note={a.lastMatchNote}
              actions={<Link to="/bidrl/watchlists">Watchlists</Link>}
            />
            <Card
              title="Settings"
              actions={<Link to="/plugins/bidrl/settings">Change them</Link>}
            >
              <Stack>
                <Hint>
                  Whether automation runs, which locations it sweeps, and how much it takes
                  per tick are plugin configuration: only the administration screen writes
                  them, so a plugin page cannot turn its own schedule on.
                </Hint>
                <Hint>
                  Run on a schedule: <strong>{a.enabled ? "yes" : "no"}</strong> · Locations
                  to sweep:{" "}
                  <strong>
                    {a.locations === 0 ? "none chosen" : automationLocations(a, labels).join(", ")}
                  </strong>{" "}
                  · At most <strong>{a.maxAuctionsPerSweep}</strong> auctions and{" "}
                  <strong>{a.maxNewLotsPerSweep}</strong> new lots a tick.
                </Hint>
              </Stack>
            </Card>
          </>
        ) : null}
      </Stack>
    </Page>
  );
}

/** One scheduled tick: when it runs, what it would do next, and what it last did. */
function TickCard({
  title,
  schedule,
  plan,
  at,
  note,
  actions,
}: {
  title: string;
  schedule: string;
  plan: string;
  at: string;
  note: string;
  actions?: ReactNode;
}) {
  return (
    <Card title={title} actions={actions ?? null}>
      <Stack>
        <div className="bidrl-automation">
          <Badge>{schedule}</Badge>
          <Hint>{plan}</Hint>
        </div>
        {at ? (
          <Hint>
            Last run <RelativeTime at={at} />
            {note ? ` — ${note}` : ""}
          </Hint>
        ) : (
          <Hint>It has not run yet.</Hint>
        )}
      </Stack>
    </Card>
  );
}
