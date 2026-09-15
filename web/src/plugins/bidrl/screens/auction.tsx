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
  Field,
  Grid,
  Link,
  Loading,
  Metric,
  Page,
  PageHeader,
  PluginDisabledError,
  Select,
  Stack,
  useNavigate,
  useQueryState,
  useRouteParams,
} from "@cc/ui";
import {
  LOT_SORT_DEFAULTS,
  cycleSort,
  lotSortParam,
  parseLotSort,
  overlayBids,
  type LotSortColumn,
} from "../model";
import { api } from "../api";
import { useInfiniteAuction, useLotView } from "../data";
import { useLiveBids, LiveDot } from "../live";
import { ViewToggle, BidrlLink, BidrlTabs } from "../chrome";
import { useAction, Notices } from "../actions";
import { LotBrowser, LotLoadMore, LotRefreshIndicator } from "../lots";

export function AuctionView() {
  const id = useRouteParams().id ?? "";
  const [sortParam, setSortParam] = useQueryState("sort");
  const sort = parseLotSort(sortParam, { column: "lot", dir: "asc" });
  const snap = useInfiniteAuction(id, lotSortParam(sort));
  const live = useLiveBids(
    snap.status === "ready" ? snap.lots : undefined,
    !(snap.error instanceof PluginDisabledError),
  );
  const [view, setView] = useLotView();
  const { busy, notice, error, run, setError } = useAction();
  const [deleting, setDeleting] = useState(false);
  const navigate = useNavigate();
  const changeSort = (column: LotSortColumn) => {
    const next = cycleSort(sort, column, LOT_SORT_DEFAULTS[column]);
    setSortParam(next ? lotSortParam(next) : "");
  };
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
  const auction = snap.latest?.auction ?? null;
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
        {snap.error && !disabled ? <Callout tone="danger">{snap.error.message || "Could not load auction."}</Callout> : null}
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
          <Card
            title="Lots"
            actions={
              <>
                <Field label="Sort">
                  <Select
                    value={lotSortParam(sort)}
                    onChange={(e) => setSortParam(e.target.value)}
                    aria-label="Sort auction lots"
                  >
                    <option value="lot.asc">Lot code</option>
                    <option value="ends.asc">Closing soon</option>
                    <option value="gap.desc">Best opportunities</option>
                    <option value="bid.asc">Lowest bid</option>
                    <option value="name.asc">Name</option>
                  </Select>
                </Field>
                <ViewToggle value={view} onChange={setView} />
              </>
            }
          >
            <>
              <div className="bidrl-lot-results">
                <LotRefreshIndicator refreshing={snap.refreshing} />
                <LotBrowser
                  lots={overlayBids(snap.lots, live.bids)}
                  empty="This auction has no lots yet."
                  view={view}
                  sort={snap.refreshing ? null : sort}
                  onSort={changeSort}
                />
              </div>
              <LotLoadMore
                hasMore={snap.hasMore}
                loading={snap.loadingMore}
                onLoadMore={snap.loadMore}
              />
            </>
          </Card>
        ) : null}
      </Stack>
    </Page>
  );
}
