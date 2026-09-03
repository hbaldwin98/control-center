/** One lot, in full. */
import {
  useMemo,
} from "react";
import {
  Button,
  Callout,
  Card,
  Countdown,
  Dash,
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
  useRouteParams,
} from "@cc/ui";
import {
  cents,
  comparableHint,
  gapTone,
  locationLabelOrEmpty,
  lotNeighbours,
  pct,
  overlayBid,
} from "../model";
import { api } from "../api";
import { remembered } from "../place";
import { useLot, useAuctionLots } from "../data";
import { useLiveBids, LiveDot } from "../live";
import { BidrlLink, BidrlTabs } from "../chrome";
import { useAction, Notices } from "../actions";
import { FavoriteStar, LotNote } from "../lotparts";
import { LotPhotos } from "../gallery";

export function LotView() {
  const id = useRouteParams().id ?? "";
  const snap = useLot(id);
  const { busy, notice, error, run } = useAction();
  const disabled = snap.error instanceof PluginDisabledError;
  const stored = snap.status === "ready" ? snap.data : null;
  // The screen someone actually watches a price on: keep it on the feed while it is up.
  const watched = useMemo(() => (stored ? [stored] : undefined), [stored]);
  const live = useLiveBids(watched, !disabled);
  const lot = stored ? overlayBid(stored, live.bids) : null;
  // Siblings come from the lot's own auction, so paging through a scan never leaves the
  // page to go back to a list and pick the next row.
  const siblings = useAuctionLots(lot?.auctionId ?? "");
  const neighbours = useMemo(() => lotNeighbours(siblings.lots, id), [siblings.lots, id]);
  return (
    <Page>
      <PageHeader
        title={lot?.title ?? "Lot"}
        lede={lot?.lotCode ? `Lot ${lot.lotCode}` : undefined}
        actions={<LiveDot status={live.status} />}
      />
      <Stack>
        <BidrlTabs />
        {lot ? (
          <div className="bidrl-crumbs">
            <Link to={remembered("/bidrl/lots")}>Lots</Link>
            <span aria-hidden="true">/</span>
            <Link to={`/bidrl/auction/${encodeURIComponent(lot.auctionId)}`}>
              {siblings.title || "Auction"}
            </Link>
            {lot.url ? (
              <>
                <span aria-hidden="true">·</span>
                <BidrlLink href={lot.url} />
              </>
            ) : null}
            <span className="bidrl-crumbs__spacer" />
            {neighbours.prev ? (
              <Link to={`/bidrl/lot/${encodeURIComponent(neighbours.prev.id)}`}>← Previous lot</Link>
            ) : null}
            {neighbours.position ? (
              <span className="bidrl-crumbs__count">{neighbours.position}</span>
            ) : null}
            {neighbours.next ? (
              <Link to={`/bidrl/lot/${encodeURIComponent(neighbours.next.id)}`}>Next lot →</Link>
            ) : null}
          </div>
        ) : null}
        <Notices message={notice} error={error} disabled={disabled} />
        {snap.status === "loading" ? <Loading label="Loading lot…" /> : null}
        {snap.status === "error" && !disabled ? <Callout tone="danger">{snap.error.message}</Callout> : null}
        {lot ? (
          <>
            <Grid density="metric">
              <Metric label="Bid" value={cents(lot.currentBidCents)} />
              <Metric
                label="Comparable"
                value={lot.priceCents != null ? cents(lot.priceCents) : "Unpriced"}
                hint={lot.priceCents != null ? comparableHint(lot) : lot.basis || undefined}
              />
              <Metric
                label="Gap"
                value={lot.dealScore != null ? pct(lot.dealScore) : <Dash />}
                tone={gapTone(lot.dealScore)}
              />
              <Metric label="Ends" value={lot.endsAt ? <Countdown iso={lot.endsAt} /> : <Dash />} />
              <Metric label="Saved" value={<FavoriteStar lot={lot} />} />
              <Metric
                label="Location"
                value={
                  locationLabelOrEmpty(lot) ? (
                    lot.affiliateId ? (
                      <Link to={`/bidrl/lots?affiliate=${encodeURIComponent(lot.affiliateId)}`}>
                        {locationLabelOrEmpty(lot)}
                      </Link>
                    ) : (
                      locationLabelOrEmpty(lot)
                    )
                  ) : (
                    <Dash />
                  )
                }
              />
            </Grid>
            <div className="bidrl-command">
              <div className="bidrl-command__jobs">
                <Button
                  variant="primary"
                  disabled={disabled || busy !== null || (lot.basis !== "exact_text" && lot.basis !== "barcode")}
                  title={
                    lot.basis !== "exact_text" && lot.basis !== "barcode"
                      ? "Repricing needs a model or barcode read from a photo. Enrich first."
                      : undefined
                  }
                  onClick={() =>
                    void run("reprice", "Reprice", () => api.post(`/lots/${encodeURIComponent(id)}/reprice`))
                  }
                >
                  {busy === "reprice" ? "Queueing…" : "Reprice"}
                </Button>
                <Button
                  disabled={disabled || busy !== null}
                  onClick={() =>
                    void run("enrich", "Enrich", () => api.post(`/lots/${encodeURIComponent(id)}/enrich`))
                  }
                >
                  {busy === "enrich" ? "Queueing…" : "Enrich"}
                </Button>
              </div>
            </div>
            {lot.photoUrls && lot.photoUrls.length > 0 ? (
              <LotPhotos key={lot.id} urls={lot.photoUrls} />
            ) : null}
            <Card title="Identification">
              <Stack>
                <p>{lot.identification || lot.title}</p>
                <Hint>
                  Basis: {lot.basis || "none"}
                  {lot.modelOrSku ? ` · ${lot.modelOrSku}` : ""}
                  {lot.category ? ` · ${lot.category}` : ""}
                  {` · title agreement ${Math.round(lot.titleAgreement * 100)}%`}
                </Hint>
                {lot.description ? <p>{lot.description}</p> : null}
              </Stack>
            </Card>
            <LotNote lot={lot} />
            <Card title="Bidding">
              <Grid density="metric">
                <Metric label="High bidder" value={lot.highBidder || <Dash />} />
                <Metric label="Bids" value={String(lot.bidCount)} />
                <Metric label="Minimum bid" value={cents(lot.minBidCents)} />
                <Metric label="Increment" value={cents(lot.bidIncrementCents)} />
                <Metric label="Reserve" value={lot.reserveMet ? "Met" : "Not met"} />
                <Metric label="Extended" value={lot.biddingExtended ? "Yes" : "No"} />
              </Grid>
            </Card>
            {lot.priceCents != null ? (
              <Card title="Where this number came from">
                <Stack>
                  <p>
                    {lot.priceKind === "sold" ? "Sold" : lot.priceKind === "asking" ? "Asking" : "Listed"}
                    {lot.sourceLabel ? ` on ${lot.sourceLabel}` : ""}
                    {": "}
                    {lot.citedText || lot.sourceTitle || "Search listing"}
                  </p>
                  {lot.sourceUrl ? (
                    <a href={lot.sourceUrl} target="_blank" rel="noreferrer">
                      {lot.sourceTitle || lot.sourceUrl}
                    </a>
                  ) : null}
                  {lot.reusedFromLotId ? (
                    <Hint>
                      Same comparable as{" "}
                      <Link to={`/bidrl/lot/${encodeURIComponent(lot.reusedFromLotId)}`}>
                        lot {lot.reusedFromLotId}
                      </Link>
                    </Hint>
                  ) : null}
                  {lot.retrievedAt ? (
                    <Hint>
                      <RelativeTime at={lot.retrievedAt} prefix="Looked up" />
                    </Hint>
                  ) : null}
                </Stack>
              </Card>
            ) : (
              <Callout>
                No numeric valuation. A number is stored only when a photo shows a model or barcode
                and a search hit — eBay sold first, then retail, then other resale — writes
                that model and a dollar amount.
              </Callout>
            )}
          </>
        ) : null}
      </Stack>
    </Page>
  );
}
