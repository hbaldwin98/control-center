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
  const opportunityLabel = lot?.dealScore != null ? `${pct(lot.dealScore)} below` : "No comparable";
  const opportunityTone = gapTone(lot?.dealScore);
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
          <div className="bidrl-crumbs bidrl-lot-nav">
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
            <section
              className={`bidrl-lot-hero${lot.photoUrls && lot.photoUrls.length > 0 ? "" : " bidrl-lot-hero--no-photo"}`}
              aria-label="Lot decision summary"
            >
              {lot.photoUrls && lot.photoUrls.length > 0 ? (
                <div className="bidrl-lot-hero__gallery">
                  <LotPhotos key={lot.id} urls={lot.photoUrls} />
                </div>
              ) : null}
              <div className="bidrl-lot-hero__decision">
                <div className="bidrl-lot-hero__eyebrow">Decision snapshot</div>
                <div className={`bidrl-lot-opportunity bidrl-lot-opportunity--${opportunityTone}`}>
                  <span>Opportunity</span>
                  <strong>{opportunityLabel}</strong>
                  <small>
                    {lot.priceCents != null ? `vs ${cents(lot.priceCents)} comparable` : "Price this lot to see the gap"}
                  </small>
                </div>
                <div className="bidrl-lot-hero__metrics">
                  <div>
                    <span>Current bid</span>
                    <strong>{cents(lot.currentBidCents)}</strong>
                    <small>{lot.bidCount} bid{lot.bidCount === 1 ? "" : "s"}</small>
                  </div>
                  <div>
                    <span>Comparable</span>
                    <strong>{lot.priceCents != null ? cents(lot.priceCents) : <Dash />}</strong>
                    <small>{lot.priceCents != null ? comparableHint(lot) || "source available" : "Unpriced"}</small>
                  </div>
                  <div>
                    <span>Closes</span>
                    <strong>{lot.endsAt ? <Countdown iso={lot.endsAt} /> : <Dash />}</strong>
                    <small>{lot.endsAt ? "Keep an eye on it" : "No close time"}</small>
                  </div>
                  <div>
                    <span>Location</span>
                    <strong>
                      {locationLabelOrEmpty(lot) ? (
                        lot.affiliateId ? (
                          <Link to={`/bidrl/lots?affiliate=${encodeURIComponent(lot.affiliateId)}`}>
                            {locationLabelOrEmpty(lot)}
                          </Link>
                        ) : (
                          locationLabelOrEmpty(lot)
                        )
                      ) : (
                        <Dash />
                      )}
                    </strong>
                    <small>{lot.category || "Auction lot"}</small>
                  </div>
                </div>
                <div className="bidrl-lot-hero__actions">
                  <div className="bidrl-lot-save">
                    <FavoriteStar lot={lot} />
                    <span>Save lot</span>
                  </div>
                  <div className="bidrl-lot-hero__jobs">
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
              </div>
            </section>
            <div className="bidrl-lot-detail-grid">
              <div className="bidrl-lot-detail-grid__main">
                <Card title="Identification" className="bidrl-lot-section bidrl-lot-identification">
                  <div className="bidrl-lot-identification__content">
                    <div className="bidrl-lot-identification__headline">
                      <span className="bidrl-lot-section__eyebrow">Matched identity</span>
                      <strong>{lot.identification || lot.title}</strong>
                    </div>
                    <dl className="bidrl-lot-facts">
                      <div>
                        <dt>Basis</dt>
                        <dd><span className="bidrl-lot-basis">{lot.basis || "Not identified"}</span></dd>
                      </div>
                      {lot.modelOrSku ? (
                        <div>
                          <dt>Model / SKU</dt>
                          <dd>{lot.modelOrSku}</dd>
                        </div>
                      ) : null}
                      {lot.category ? (
                        <div>
                          <dt>Category</dt>
                          <dd>{lot.category}</dd>
                        </div>
                      ) : null}
                      {lot.titleAgreement > 0 ? (
                        <div>
                          <dt>Title agreement</dt>
                          <dd>{Math.round(lot.titleAgreement * 100)}%</dd>
                        </div>
                      ) : null}
                    </dl>
                    {lot.description ? <p className="bidrl-lot-description">{lot.description}</p> : null}
                  </div>
                </Card>
                <Card title="Auction details" className="bidrl-lot-section bidrl-lot-bidding">
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
                  <Card title="Comparable evidence" className="bidrl-lot-section bidrl-lot-evidence">
                    <div className="bidrl-lot-evidence__summary">
                      <div>
                        <span className="bidrl-lot-section__eyebrow">Reference value</span>
                        <strong>{cents(lot.priceCents)}</strong>
                      </div>
                      <span className="bidrl-lot-evidence__kind">
                        {lot.priceKind === "sold" ? "Sold" : lot.priceKind === "asking" ? "Asking" : "Listed"}
                        {lot.sourceLabel ? ` · ${lot.sourceLabel}` : ""}
                      </span>
                    </div>
                    <blockquote className="bidrl-lot-evidence__quote">
                      {lot.citedText || lot.sourceTitle || "Search listing"}
                    </blockquote>
                    <div className="bidrl-lot-evidence__footer">
                      {lot.sourceUrl ? (
                        <a href={lot.sourceUrl} target="_blank" rel="noreferrer">
                          Open source ↗
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
                    </div>
                  </Card>
                ) : (
                  <div className="bidrl-lot-no-evidence">
                    <Callout>
                      No numeric valuation yet. A number is stored only when a photo shows a model or barcode
                      and a search hit — eBay sold first, then retail, then other resale — writes that model
                      and a dollar amount.
                    </Callout>
                  </div>
                )}
              </div>
              <aside className="bidrl-lot-detail-grid__aside" aria-label="Saved lot note">
                <LotNote lot={lot} />
              </aside>
            </div>
          </>
        ) : null}
      </Stack>
    </Page>
  );
}
