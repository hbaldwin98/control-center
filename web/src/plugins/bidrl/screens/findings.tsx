import {
  useState,
} from "react";
import {
  Badge,
  Button,
  Callout,
  Card,
  Countdown,
  EmptyState,
  Field,
  Hint,
  Link,
  Loading,
  Page,
  PageHeader,
  PluginDisabledError,
  RelativeTime,
  Select,
  Stack,
  Toolbar,
  useQueryState,
} from "@cc/ui";
import {
  cents,
  groupFindings,
  type Finding,
} from "../model";
import { api } from "../api";
import { usePlace } from "../place";
import { useFindings } from "../data";
import { BidrlTabs } from "../chrome";
import { Notices } from "../actions";
import { LotThumbLink, LotMeta, LotLocation, LotTitle } from "../lotparts";

const FINDING_STATES = [
  { value: "new", label: "To review" },
  { value: "accepted", label: "Accepted" },
  { value: "rejected", label: "Rejected" },
  { value: "all", label: "Everything" },
] as const;

/**
 * One finding, as a decision rather than a listing. The judge's sentence is shown as
 * the model's words, attributed — it is why this reached the queue, not a fact about
 * the lot.
 */
function FindingRow({
  finding,
  onDecide,
  busy,
}: {
  finding: Finding;
  onDecide: (state: "accepted" | "rejected") => void;
  busy: boolean;
}) {
  return (
    <div className="bidrl-finding">
      <LotThumbLink lot={finding.lot} />
      <div className="bidrl-finding__body">
        <LotTitle lot={finding.lot} />
        <div className="bidrl-finding__meta">
          {cents(finding.lot.currentBidCents)}
          {finding.lot.endsAt ? (
            <>
              {" · "}
              <Countdown iso={finding.lot.endsAt} />
            </>
          ) : null}
          <LotLocation lot={finding.lot} />
          <LotMeta lot={finding.lot} />
        </div>
        {finding.reason ? (
          <p className="bidrl-finding__reason">
            <span className="bidrl-finding__said">Matched because</span> {finding.reason}
          </p>
        ) : null}
      </div>
      <div className="bidrl-finding__signal" aria-label={`Score ${finding.score}`}>
        <strong>{finding.score.toFixed(2)}</strong>
        <small>score</small>
      </div>
      <div className="bidrl-finding__date">
        <RelativeTime at={finding.createdAt} />
      </div>
      {finding.state === "new" ? (
        <div className="bidrl-finding__actions">
          <Button variant="primary" size="sm" disabled={busy} onClick={() => onDecide("accepted")}>
            Accept
          </Button>
          <Button size="sm" disabled={busy} onClick={() => onDecide("rejected")}>
            Reject
          </Button>
        </div>
      ) : (
        <div className="bidrl-finding__actions">
          <Badge tone={finding.state === "accepted" ? "ok" : "neutral"}>{finding.state}</Badge>
        </div>
      )}
    </div>
  );
}

/**
 * The review queue. Grouped by watchlist, newest first, with Accept and Reject as
 * direct writes — they are instant and local, so they do not queue a job.
 *
 * Accepting saves the lot as well as clearing it, because accepting something and then
 * hunting for it in another tab is the obvious wrong flow. A rejection is permanent:
 * the same lot never comes back to this queue for the same watchlist, and later runs
 * skip it before it costs a call.
 */
export function Findings() {
  const [state, setState] = useQueryState("state", "new");
  const [watchlist, setWatchlist] = useQueryState("watchlist");
  const snap = useFindings(state, watchlist);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const disabled = snap.error instanceof PluginDisabledError;
  usePlace("/bidrl/findings", snap.status === "ready");

  const decide = async (id: string, next: "accepted" | "rejected") => {
    setBusy(id);
    setError(null);
    try {
      await api.post(`/findings/${encodeURIComponent(id)}/${next === "accepted" ? "accept" : "reject"}`);
      snap.reload();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not record that.");
    } finally {
      setBusy(null);
    }
  };

  const groups = snap.status === "ready" ? groupFindings(snap.data.findings) : [];
  const watchlists = snap.status === "ready" ? snap.data.watchlists : [];

  return (
    <Page>
      <PageHeader
        eyebrow="BidRL"
        title="Findings"
        lede="What your watchlists turned up, waiting on you. Accepting saves the lot; rejecting is permanent, and the same lot never costs another call for that watchlist."
      />
      <Stack>
        <BidrlTabs />
        <Notices message={null} error={error} disabled={disabled} />
        <Card
          title="Review queue"
          className="bidrl-surface bidrl-findings-surface"
          actions={<Link to="/bidrl/watchlists">Watchlists</Link>}
        >
          <Toolbar>
            <Field label="Show">
              <Select value={state} onChange={(e) => setState(e.target.value)} aria-label="State">
                {FINDING_STATES.map((s) => (
                  <option key={s.value} value={s.value}>{s.label}</option>
                ))}
              </Select>
            </Field>
            <Field label="Watchlist">
              <Select
                value={watchlist}
                onChange={(e) => setWatchlist(e.target.value)}
                aria-label="Watchlist"
              >
                <option value="">All watchlists</option>
                {watchlists.map((w) => (
                  <option key={w.id} value={w.id}>{w.name}</option>
                ))}
              </Select>
            </Field>
          </Toolbar>
          {snap.status === "loading" ? <Loading label="Loading findings…" /> : null}
          {snap.status === "error" && !disabled ? (
            <Callout tone="danger">{snap.error.message}</Callout>
          ) : null}
          {snap.status === "ready" && snap.data.findings.length === 0 ? (
            <EmptyState>
              {watchlists.length === 0
                ? "No watchlists yet. Describe what you are looking for and run it — nothing happens on its own."
                : state === "new"
                  ? "Nothing waiting. Run a watchlist to look again."
                  : "No findings in that state."}
            </EmptyState>
          ) : null}
          {groups.map((group) => (
            <div key={group.id} className="bidrl-finding-group">
              <h3 className="bidrl-finding-group__name">
                {group.label} <Hint>{group.findings.length}</Hint>
              </h3>
              {group.findings.map((f) => (
                <FindingRow
                  key={f.id}
                  finding={f}
                  busy={busy === f.id}
                  onDecide={(next) => void decide(f.id, next)}
                />
              ))}
            </div>
          ))}
        </Card>
      </Stack>
    </Page>
  );
}
