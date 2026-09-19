import {
  Callout,
  Card,
  Countdown,
  Grid,
  Hint,
  Link,
  Loading,
  Page,
  PageHeader,
  PluginDisabledError,
  Stack,
  useQueryState,
} from "@cc/ui";
import { useOverview } from "../data";
import { BidrlTabs, StatLink } from "../chrome";
import { Notices } from "../actions";
import { LotBrowser } from "../lots";
import { AutomationStrip } from "../schedule";
import { FavoriteStar, LotThumb } from "../lotparts";
import { cents, comparableHint, locationLabelOrEmpty, pct, type Lot } from "../model";

/**
 * The front door. It answers "is there anything to look at" without being a fourth
 * listing of the same table: a few counts, the widest gaps, what closes next, and a link
 * into the catalog for each. Everything here is a shortcut to a filtered catalog URL.
 */
export function Overview() {
  const snap = useOverview();
  const [selectedId, setSelectedId] = useQueryState("lot");
  const disabled = snap.error instanceof PluginDisabledError;
  const stats = snap.status === "ready" ? snap.data.stats : null;
  const deals = snap.status === "ready" ? snap.data.deals : [];
  const closing = snap.status === "ready" ? snap.data.closing : [];
  const selected =
    closing.find((lot) => lot.id === selectedId) ??
    deals.find((lot) => lot.id === selectedId) ??
    closing[0] ??
    deals[0] ??
    null;

  return (
    <Page>
      <PageHeader
        eyebrow="BidRL"
        title="Overview"
        lede="Auction intelligence from collected lots, photographs, and comparable prices."
      />
      <Stack>
        <BidrlTabs />
        <Notices message={null} error={null} disabled={disabled} />
        <AutomationStrip />
        {snap.status === "loading" ? <Loading label="Loading…" /> : null}
        {snap.status === "error" && !disabled ? (
          <Callout tone="danger">{snap.error.message}</Callout>
        ) : null}
        {stats != null && stats.lots === 0 ? (
          <Card title="Start here">
            <Stack>
              <Hint>
                Nothing is collected yet. Collect an auction, then scan it —
                scanning is what reads the photographs and produces comparables.
              </Hint>
              <div className="bidrl-actions">
                <Link
                  to="/bidrl/auctions"
                  className="cc-button cc-button--primary"
                >
                  Collect an auction
                </Link>
              </div>
            </Stack>
          </Card>
        ) : null}
        {stats != null && stats.lots > 0 ? (
          <>
            <div className="bidrl-overview__metrics">
              <Grid density="metric">
                <StatLink
                  to="/bidrl/auctions"
                  label="Collected auctions"
                  value={String(stats.auctions)}
                />
                <StatLink
                  to="/bidrl/lots"
                  label="Open lots"
                  value={String(stats.lots)}
                  hint={`${stats.live} still open`}
                />
                <StatLink
                  to="/bidrl/lots?filter=deals"
                  label="Priced"
                  value={String(stats.priced)}
                  hint={
                    stats.unscanned > 0
                      ? `${stats.unscanned} never scanned`
                      : "all scanned"
                  }
                  tone={stats.priced > 0 ? "ok" : "neutral"}
                />
                <StatLink
                  to="/bidrl/lots?ending=soon"
                  label="Ending in 24h"
                  value={String(stats.ending)}
                  tone={stats.ending > 0 ? "warn" : "neutral"}
                />
              </Grid>
            </div>
            <div className="bidrl-overview__grid">
              <Card title="Needs attention" className="bidrl-surface bidrl-overview__panel">
                <div className="bidrl-attention-list">
                  {stats.unscanned > 0 ? (
                    <div className="bidrl-attention-list__item">
                      <span>
                        <strong>{stats.unscanned}</strong>
                        <small>lots waiting for a scan</small>
                      </span>
                      <Link to="/bidrl/lots">Review</Link>
                    </div>
                  ) : null}
                  {stats.ending > 0 ? (
                    <div className="bidrl-attention-list__item">
                      <span>
                        <strong>{stats.ending}</strong>
                        <small>lots closing in 24 hours</small>
                      </span>
                      <Link to="/bidrl/lots?ending=soon">Open</Link>
                    </div>
                  ) : null}
                  {stats.priced > 0 ? (
                    <div className="bidrl-attention-list__item">
                      <span>
                        <strong>{stats.priced}</strong>
                        <small>priced opportunities</small>
                      </span>
                      <Link to="/bidrl/lots?filter=deals">Review</Link>
                    </div>
                  ) : null}
                  {stats.unscanned === 0 && stats.ending === 0 && stats.priced === 0 ? (
                    <p className="bidrl-overview__empty">Nothing needs attention right now.</p>
                  ) : null}
                </div>
              </Card>
              <Card title="System pulse" className="bidrl-surface bidrl-overview__panel">
                <div className="bidrl-pulse-grid">
                  <div>
                    <span>Scanned</span>
                    <strong>{stats.scanned}</strong>
                  </div>
                  <div>
                    <span>Live lots</span>
                    <strong>{stats.live}</strong>
                  </div>
                  <div>
                    <span>Unscanned</span>
                    <strong>{stats.unscanned}</strong>
                  </div>
                </div>
                <Link className="bidrl-panel-link" to="/bidrl/automation">Open automation</Link>
              </Card>
            </div>
            <Card
              title="Closing next"
              className="bidrl-surface bidrl-closing-surface"
              actions={<Link to="/bidrl/lots?ending=soon">See all closing lots</Link>}
            >
              <ClosingWorkspace
                lots={closing}
                selected={selected}
                selectedId={selectedId}
                onSelect={setSelectedId}
              />
            </Card>
            <Card
              title="Plugin pulse"
              className="bidrl-surface"
              actions={<Link to="/bidrl/lots?filter=deals">See all priced lots</Link>}
            >
              <LotBrowser
                lots={deals}
                empty="No lot has a comparable yet. Scan an auction, then reprice a lot whose photos show a model."
                view="grid"
                groupSimilar={false}
              />
            </Card>
          </>
        ) : null}
      </Stack>
    </Page>
  );
}

function ClosingWorkspace({
  lots,
  selected,
  selectedId,
  onSelect,
}: {
  lots: Lot[];
  selected: Lot | null;
  selectedId: string;
  onSelect: (id: string) => void;
}) {
  if (lots.length === 0) {
    return <Hint>Nothing collected closes in the next week.</Hint>;
  }

  return (
    <div className="bidrl-closing-layout">
      <div className="bidrl-closing-list" role="list" aria-label="Lots closing next">
        {lots.map((lot) => {
          const active = selected?.id === lot.id;
          return (
            <Link
              key={lot.id}
              to={`/bidrl?lot=${encodeURIComponent(lot.id)}`}
              className={`bidrl-closing-row${active ? " bidrl-closing-row--selected" : ""}`}
              aria-current={active && selectedId ? "true" : undefined}
              onClick={() => onSelect(lot.id)}
            >
              <LotThumb lot={lot} className="bidrl-closing-row__thumb" />
              <span className="bidrl-closing-row__item">
                <strong>{lot.title || lot.id}</strong>
                <small>
                  {lot.lotCode || "Lot"} · {locationLabelOrEmpty(lot) || "Location unknown"}
                </small>
              </span>
              <span className="bidrl-closing-row__bid">
                <strong>{cents(lot.currentBidCents)}</strong>
                <small>{lot.endsAt ? <Countdown iso={lot.endsAt} /> : "No close time"}</small>
              </span>
            </Link>
          );
        })}
      </div>
      {selected ? <SelectedLot lot={selected} /> : null}
    </div>
  );
}

function SelectedLot({ lot }: { lot: Lot }) {
  return (
    <aside className="bidrl-selected-lot" aria-label="Selected lot">
      <div className="bidrl-selected-lot__head">
        <span className="bidrl-eyebrow">Selected lot</span>
        <FavoriteStar lot={lot} />
      </div>
      <div className="bidrl-selected-lot__photo">
        <LotThumb lot={lot} className="bidrl-selected-lot__img" />
      </div>
      <div className="bidrl-selected-lot__body">
        <Link className="bidrl-selected-lot__title" to={`/bidrl/lot/${encodeURIComponent(lot.id)}`}>
          {lot.title || lot.id}
        </Link>
        <p>
          {lot.identification && lot.identification !== lot.title ? lot.identification : lot.lotCode || "Lot"}
          {locationLabelOrEmpty(lot) ? ` · ${locationLabelOrEmpty(lot)}` : ""}
        </p>
        <div className="bidrl-selected-lot__facts">
          <div>
            <span>Current bid</span>
            <strong>{cents(lot.currentBidCents)}</strong>
          </div>
          <div>
            <span>Bids</span>
            <strong>{lot.bidCount}</strong>
          </div>
          <div>
            <span>Comparable</span>
            <strong>{lot.priceCents == null ? "None" : cents(lot.priceCents)}</strong>
          </div>
          <div>
            <span>Opportunity</span>
            <strong>{lot.dealScore == null ? "—" : `${pct(lot.dealScore)} below`}</strong>
          </div>
        </div>
        {comparableHint(lot) ? <small className="bidrl-selected-lot__source">{comparableHint(lot)}</small> : null}
      </div>
      <Link className="bidrl-selected-lot__open" to={`/bidrl/lot/${encodeURIComponent(lot.id)}`}>
        Open lot detail
      </Link>
    </aside>
  );
}
