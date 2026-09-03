/** One auction and its lots. */
import {
  useState,
} from "react";
import {
  Button,
  Callout,
  Card,
  Countdown,
  Dash,
  Grid,
  Link,
  Loading,
  Metric,
  Page,
  PageHeader,
  PluginDisabledError,
  Stack,
  useNavigate,
  useRouteParams,
} from "@cc/ui";
import {
  overlayBids,
} from "../model";
import { api } from "../api";
import { useAuction, useLotView } from "../data";
import { useLiveBids, LiveDot } from "../live";
import { ViewToggle, BidrlLink, BidrlTabs } from "../chrome";
import { useAction, Notices } from "../actions";
import { LotBrowser } from "../lots";

export function AuctionView() {
  const id = useRouteParams().id ?? "";
  const snap = useAuction(id);
  const live = useLiveBids(
    snap.status === "ready" ? snap.data.lots : undefined,
    !(snap.error instanceof PluginDisabledError),
  );
  const [view, setView] = useLotView();
  const { busy, notice, error, run, setError } = useAction();
  const [deleting, setDeleting] = useState(false);
  const navigate = useNavigate();
  const remove = async () => {
    if (!window.confirm("Delete this auction? Its lots, photos, analyses, and comparables go with it.")) {
      return;
    }
    setDeleting(true);
    setError(null);
    try {
      await api.del(`/auctions/${encodeURIComponent(id)}`);
      navigate("/bidrl/auctions");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setDeleting(false);
    }
  };
  const pending = busy !== null || deleting;
  const disabled = snap.error instanceof PluginDisabledError;
  const auction = snap.status === "ready" ? snap.data.auction : null;
  return (
    <Page>
      <PageHeader
        title={auction?.title ?? "Auction"}
        lede={auction ? undefined : "Auction"}
        actions={<LiveDot status={live.status} />}
      />
      <Stack>
        <BidrlTabs />
        <div className="bidrl-crumbs">
          <Link to="/bidrl/auctions">Auctions</Link>
          <span className="bidrl-crumbs__spacer" />
          {auction?.url ? <BidrlLink href={auction.url} /> : null}
        </div>
        <Notices message={notice} error={error} disabled={disabled} />
        {snap.status === "loading" ? <Loading label="Loading auction…" /> : null}
        {snap.status === "error" && !disabled ? <Callout tone="danger">{snap.error.message}</Callout> : null}
        {auction ? (
          <Grid density="metric">
            <Metric label="Lots" value={String(auction.lotCount)} />
            <Metric label="Status" value={auction.status} />
            <Metric label="Ends" value={auction.endsAt ? <Countdown iso={auction.endsAt} /> : <Dash />} />
          </Grid>
        ) : null}
        <div className="bidrl-command">
          <div className="bidrl-command__jobs">
            <Button
              variant="primary"
              disabled={disabled || pending}
              onClick={() =>
                void run("scan", "Scan", () => api.post(`/auctions/${encodeURIComponent(id)}/scan`))
              }
            >
              {busy === "scan" ? "Queueing…" : "Scan"}
            </Button>
            <Button
              disabled={disabled || pending}
              onClick={() =>
                void run("refresh", "Bid refresh", () =>
                  api.post(`/auctions/${encodeURIComponent(id)}/refresh`),
                )
              }
            >
              {busy === "refresh" ? "Queueing…" : "Refresh bids"}
            </Button>
          </div>
          <Button variant="danger" disabled={disabled || pending} onClick={() => void remove()}>
            {deleting ? "Deleting…" : "Delete"}
          </Button>
        </div>
        {snap.status === "ready" ? (
          <Card title="Lots" actions={<ViewToggle value={view} onChange={setView} />}>
            <LotBrowser
              lots={overlayBids(snap.data.lots, live.bids)}
              empty="This auction has no lots yet."
              view={view}
            />
          </Card>
        ) : null}
      </Stack>
    </Page>
  );
}
