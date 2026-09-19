/** One lot, in full. The same body serves the page and the drawer. */
import { useMemo } from "react";
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
import { FavoriteStar, LotLink, LotNote } from "../lotparts";
import { LotPhotos } from "../gallery";

/**
 * Everything one lot is: its snapshot, its live bids, its auction siblings, and where it
 * sits among them. Shared by the full page and the drawer so the two never drift.
 */
export function useLotDetail(id: string) {
  const snap = useLot(id);
  const action = useAction();
  const disabled = snap.error instanceof PluginDisabledError;
  const stored = snap.status === "ready" ? snap.data : null;
  // The screen someone actually watches a price on: keep it on the feed while it is up.
  const watched = useMemo(() => (stored ? [stored] : undefined), [stored]);
  const live = useLiveBids(watched, !disabled);
  const lot = stored ? overlayBid(stored, live.bids) : null;
  // Siblings come from the lot's own auction, so paging through a scan never leaves the
  // page to go back to a list and pick the next row.
  const siblings = useAuctionLots(lot?.auctionId ?? "");
  const neighbours = useMemo(
    () => lotNeighbours(siblings.lots, id),
    [siblings.lots, id],
  );
  const opportunityLabel =
    lot?.dealScore == null ? "No comparable" : `${pct(lot.dealScore)} below`;
  const opportunityTone = gapTone(lot?.dealScore);
  return {
    snap,
    action,
    disabled,
    live,
    lot,
    siblings,
    neighbours,
    opportunityLabel,
    opportunityTone,
  };
}

export type LotDetailState = ReturnType<typeof useLotDetail>;

function LotNeighbours({
  neighbours,
}: {
  neighbours: LotDetailState["neighbours"];
}) {
  if (!neighbours.prev && !neighbours.position && !neighbours.next) return null;
  return (
    <nav className="bidrl-lot-nav__neighbors" aria-label="Lot navigation">
      {neighbours.prev ? (
        <LotLink lot={neighbours.prev}>← Previous lot</LotLink>
      ) : null}
      {neighbours.position ? (
        <span className="bidrl-crumbs__count">{neighbours.position}</span>
      ) : null}
      {neighbours.next ? (
        <LotLink lot={neighbours.next}>Next lot →</LotLink>
      ) : null}
    </nav>
  );
}

/**
 * The lot body. `chrome` is the full page's breadcrumb trail; the drawer opens over a
 * list, so there it shows only the prev/next controls.
 */
export function LotDetail({
  detail,
  chrome = true,
}: {
  detail: LotDetailState;
  chrome?: boolean;
}) {
  const {
    snap,
    action,
    disabled,
    lot,
    siblings,
    neighbours,
    opportunityLabel,
    opportunityTone,
  } = detail;
  const { busy, notice, error, run } = action;
  const hasNeighbours = Boolean(
    neighbours.prev || neighbours.position || neighbours.next,
  );
  return (
    <>
      {lot && chrome ? (
        <div className="bidrl-crumbs bidrl-lot-nav">
          <Link to={remembered("/bidrl/lots")}>Lots</Link>
          <span aria-hidden="true">/</span>
          <Link to={`/bidrl/auction/${encodeURIComponent(lot.auctionId)}`}>
            {siblings.title || "Auction"}
          </Link>
          {hasNeighbours ? (
            <>
              <span className="bidrl-crumbs__spacer" />
              <LotNeighbours neighbours={neighbours} />
            </>
          ) : null}
        </div>
      ) : null}
      {lot && !chrome ? <LotNeighbours neighbours={neighbours} /> : null}
      <Notices message={notice} error={error} disabled={disabled} />
      {snap.status === "loading" ? <Loading label="Loading lot…" /> : null}
      {snap.status === "error" && !disabled ? (
        <Callout tone="danger">{snap.error.message}</Callout>
      ) : null}
      {lot ? (
        <>
          <div className="bidrl-lot-workspace">
            {lot.photoUrls && lot.photoUrls.length > 0 ? (
              <section className="bidrl-lot-hero" aria-label="Lot photos">
                <div className="bidrl-lot-hero__gallery">
                  <LotPhotos key={lot.id} urls={lot.photoUrls} />
                </div>
              </section>
            ) : null}
            <Card title="Current listing" className="bidrl-lot-current bidrl-surface">
              <div className="bidrl-lot-current__metrics">
                <div className="bidrl-lot-current__identity">
                  <span>Identity</span>
                  <strong>{lot.title || <Dash />}</strong>
                  <small>What BidRL says</small>
                </div>
                <div>
                  <span>Price</span>
                  <strong>
                    {lot.priceCents == null ? <Dash /> : cents(lot.priceCents)}
                  </strong>
                  <small>
                    {lot.priceCents == null
                      ? "No estimate"
                      : comparableHint(lot) || "Comparable"}
                  </small>
                </div>
                <div>
                  <span>Bids</span>
                  <strong>{lot.bidCount}</strong>
                  <small>
                    {lot.bidCount === 1 ? "1 bid" : `${lot.bidCount} bids`}
                  </small>
                </div>
                <div>
                  <span>Bid price</span>
                  <strong>{cents(lot.currentBidCents)}</strong>
                  <small>Current bid</small>
                </div>
                <div>
                  <span>Time remaining</span>
                  <strong>
                    {lot.endsAt ? <Countdown iso={lot.endsAt} /> : <Dash />}
                  </strong>
                  <small>
                    {lot.endsAt ? "Auction close" : "No close time"}
                  </small>
                </div>
              </div>
              {lot.url ? (
                <div className="bidrl-lot-current__source">
                  <BidrlLink href={lot.url}>Open on BidRL ↗</BidrlLink>
                </div>
              ) : null}
              <div className="bidrl-lot-hero__actions">
                <div className="bidrl-lot-save">
                  <FavoriteStar lot={lot} />
                  <span>Save lot</span>
                </div>
                <div className="bidrl-lot-hero__jobs">
                  <Button
                    variant="primary"
                    disabled={
                      disabled ||
                      busy !== null ||
                      (lot.basis !== "exact_text" && lot.basis !== "barcode")
                    }
                    title={
                      lot.basis !== "exact_text" && lot.basis !== "barcode"
                        ? "Repricing needs a model or barcode read from a photo. Enrich first."
                        : undefined
                    }
                    onClick={() =>
                      void run("reprice", "Reprice", () =>
                        api.post(`/lots/${encodeURIComponent(lot.id)}/reprice`),
                      )
                    }
                  >
                    {busy === "reprice" ? "Queueing…" : "Reprice"}
                  </Button>
                  <Button
                    disabled={disabled || busy !== null}
                    onClick={() =>
                      void run("enrich", "Enrich", () =>
                        api.post(`/lots/${encodeURIComponent(lot.id)}/enrich`),
                      )
                    }
                  >
                    {busy === "enrich" ? "Queueing…" : "Enrich"}
                  </Button>
                </div>
              </div>
            </Card>
          </div>
          <div className="bidrl-lot-detail-grid">
            <div className="bidrl-lot-detail-grid__main">
              <Card
                title="Item details"
                className="bidrl-lot-section bidrl-lot-identification"
              >
                <div className="bidrl-lot-identification__content">
                  <div className="bidrl-lot-identification__headline">
                    <span className="bidrl-lot-section__eyebrow">
                      Matched identity
                    </span>
                    <strong>{lot.identification || lot.title}</strong>
                  </div>
                  <dl className="bidrl-lot-facts">
                    <div>
                      <dt>Basis</dt>
                      <dd>
                        <span className="bidrl-lot-basis">
                          {lot.basis || "Not identified"}
                        </span>
                      </dd>
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
                  {lot.description ? (
                    <p className="bidrl-lot-description">{lot.description}</p>
                  ) : null}
                </div>
              </Card>
              <Card
                title="Auction details"
                className="bidrl-lot-section bidrl-lot-bidding"
              >
                <Grid density="metric">
                  <Metric
                    label="High bidder"
                    value={lot.highBidder || <Dash />}
                  />
                  <Metric
                    label="Minimum bid"
                    value={cents(lot.minBidCents)}
                  />
                  <Metric
                    label="Increment"
                    value={cents(lot.bidIncrementCents)}
                  />
                  <Metric
                    label="Reserve"
                    value={lot.reserveMet ? "Met" : "Not met"}
                  />
                  <Metric
                    label="Extended"
                    value={lot.biddingExtended ? "Yes" : "No"}
                  />
                </Grid>
              </Card>
              <Card
                title="Decision report"
                className="bidrl-lot-section bidrl-lot-report"
              >
                <div
                  className={`bidrl-lot-opportunity bidrl-lot-opportunity--${opportunityTone}`}
                >
                  <span>Opportunity</span>
                  <strong>{opportunityLabel}</strong>
                  <small>
                    {lot.priceCents == null
                      ? "Price this lot to see the gap"
                      : `vs ${cents(lot.priceCents)} comparable`}
                  </small>
                </div>
                <div className="bidrl-lot-hero__metrics">
                  <div>
                    <span>Comparable</span>
                    <strong>
                      {lot.priceCents == null ? (
                        <Dash />
                      ) : (
                        cents(lot.priceCents)
                      )}
                    </strong>
                    <small>
                      {lot.priceCents == null
                        ? "Unpriced"
                        : comparableHint(lot) || "source available"}
                    </small>
                  </div>
                  <div>
                    <span>Location</span>
                    <strong>
                      {locationLabelOrEmpty(lot) ? (
                        lot.affiliateId ? (
                          <Link
                            to={`/bidrl/lots?affiliate=${encodeURIComponent(lot.affiliateId)}`}
                          >
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
              </Card>
              {lot.priceCents == null ? (
                <div className="bidrl-lot-no-evidence">
                  <Callout>
                    No numeric valuation yet. A number is stored only when a
                    photo shows a model or barcode and a search hit — eBay
                    sold first, then retail, then other resale — writes that
                    model and a dollar amount.
                  </Callout>
                </div>
              ) : (
                <Card
                  title="Comparable evidence"
                  className="bidrl-lot-section bidrl-lot-evidence"
                >
                  <div className="bidrl-lot-evidence__summary">
                    <div>
                      <span className="bidrl-lot-section__eyebrow">
                        Reference value
                      </span>
                      <strong>{cents(lot.priceCents)}</strong>
                    </div>
                    <span className="bidrl-lot-evidence__kind">
                      {lot.priceKind === "sold"
                        ? "Sold"
                        : lot.priceKind === "asking"
                          ? "Asking"
                          : "Listed"}
                      {lot.sourceLabel ? ` · ${lot.sourceLabel}` : ""}
                    </span>
                  </div>
                  <blockquote className="bidrl-lot-evidence__quote">
                    {lot.citedText || lot.sourceTitle || "Search listing"}
                  </blockquote>
                  <div className="bidrl-lot-evidence__footer">
                    {lot.sourceUrl ? (
                      <a
                        href={lot.sourceUrl}
                        target="_blank"
                        rel="noreferrer"
                      >
                        Open source ↗
                      </a>
                    ) : null}
                    {lot.reusedFromLotId ? (
                      <Hint>
                        Same comparable as{" "}
                        <LotLink
                          lot={{
                            id: lot.reusedFromLotId,
                            title: `lot ${lot.reusedFromLotId}`,
                          }}
                        >
                          lot {lot.reusedFromLotId}
                        </LotLink>
                      </Hint>
                    ) : null}
                    {lot.retrievedAt ? (
                      <Hint>
                        <RelativeTime
                          at={lot.retrievedAt}
                          prefix="Looked up"
                        />
                      </Hint>
                    ) : null}
                  </div>
                </Card>
              )}
            </div>
            <aside
              className="bidrl-lot-detail-grid__aside"
              aria-label="Saved lot note"
            >
              <LotNote lot={lot} />
            </aside>
          </div>
        </>
      ) : null}
    </>
  );
}

/** The full page, for a lot opened directly or reloaded on its own URL. */
export function LotView() {
  const id = useRouteParams().id ?? "";
  const detail = useLotDetail(id);
  const { live, lot } = detail;
  return (
    <Page>
      <PageHeader
        eyebrow="BidRL / Lots"
        title={lot?.title ?? "Lot"}
        actions={<LiveDot status={live.status} />}
      />
      <Stack>
        <BidrlTabs />
        <LotDetail detail={detail} />
      </Stack>
    </Page>
  );
}
