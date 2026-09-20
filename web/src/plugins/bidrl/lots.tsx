/** A list of lots, as cards or as a table, with near-identical lots folded together. */
import { memo, useEffect, useMemo, useRef, useState } from "react";
import {
  Button,
  Countdown,
  Dash,
  EmptyState,
  Hint,
  useNow,
} from "@cc/ui";
import {
  LOT_SORT_DEFAULTS,
  cents,
  gapTone,
  groupSimilarLots,
  hasEnded,
  locationLabelOrEmpty,
  pct,
  sortLotGroups,
  type Lot,
  type LotSortColumn,
  type SimilarGroup,
  type SortState,
} from "./model";
import { useColumnSort, SortedHead } from "./sorting";
import { FavoriteStar, LotLink, LotThumbLink, bucketTone } from "./lotparts";

/** The one-word state the design prints in the table, derived only from real fields. */
function lotStatus(lot: Lot): { label: string; tone: string } {
  if (lot.priceCents == null) return { label: "unpriced", tone: "warn" };
  if (lot.bucket === "priced") return { label: "priced", tone: "success" };
  const tone = bucketTone(lot.bucket);
  const className = tone === "ok" ? "success" : tone === "neutral" ? "" : tone;
  return { label: lot.bucket.replace(/_/g, " "), tone: className };
}

/** Site and city, split so a row can print a strong line and a quiet one. */
function lotSource(lot: Lot): { site: string; city: string } {
  const city = locationLabelOrEmpty(lot);
  const site =
    lot.affiliateName && lot.affiliateName !== city ? lot.affiliateName : city;
  return { site, city: site === city ? "" : city };
}

type LotTableRowProps = {
  lot: Lot;
  extraClass?: string | undefined;
  showWhy?: boolean | undefined;
  showSaved?: boolean | undefined;
  similarCount?: number | undefined;
  similarOpen?: boolean | undefined;
  onToggleSimilar?: (() => void) | undefined;
};

function LotTableRow({
  lot,
  extraClass,
  showWhy = false,
  showSaved = false,
  similarCount = 0,
  similarOpen = false,
  onToggleSimilar,
}: LotTableRowProps) {
  const status = lotStatus(lot);
  const { site, city } = lotSource(lot);
  return (
    <tr className={extraClass}>
      <td>
        <div className="lot-main">
          <LotThumbLink lot={lot} className="thumb" />
          <div className="lot-title">
            <strong>
              <LotLink lot={lot} />
            </strong>
            <span>
              {[lot.lotCode ? `Lot ${lot.lotCode}` : "", lot.category]
                .filter(Boolean)
                .join(" · ")}
            </span>
            {showSaved && lot.favoriteNote ? (
              <span className="bidrl-table-cell__note">{lot.favoriteNote}</span>
            ) : null}
          </div>
          <FavoriteStar lot={lot} />
        </div>
      </td>
      <td className="bidrl-lot-table__source">
        {site || <Dash />}
        {city ? <small>{city}</small> : null}
      </td>
      <td className="bidrl-lot-table__bid">{cents(lot.currentBidCents)}</td>
      <td className="bidrl-lot-table__bids">{lot.bidCount ?? 0}</td>
      <td className="bidrl-lot-table__closes">
        {lot.endsAt ? <Countdown iso={lot.endsAt} /> : <Dash />}
      </td>
      <td className="bidrl-lot-table__status">
        <div className="bidrl-lot-table__status-inner">
          <span className={`pill${status.tone ? ` ${status.tone}` : ""}`}>
            {status.label}
          </span>
          {similarCount > 0 ? (
            <button
              type="button"
              className="table-action bidrl-similar-toggle"
              onClick={onToggleSimilar}
            >
              {similarOpen ? "Hide similar" : `${similarCount} similar`}
            </button>
          ) : null}
        </div>
      </td>
      {showWhy ? (
        <td className="bidrl-lot-table__why">{lot.matchReason || <Dash />}</td>
      ) : null}
      <td className="bidrl-lot-table__open">
        <LotLink className="table-action" lot={lot}>
          Open
        </LotLink>
      </td>
    </tr>
  );
}

export function LotTableRows({
  lots,
  extraClass,
  showWhy = false,
  showSaved = false,
}: {
  lots: Lot[];
  extraClass?: string;
  showWhy?: boolean;
  showSaved?: boolean;
}) {
  return (
    <>
      {lots.map((lot) => (
        <LotTableRow
          key={lot.id}
          lot={lot}
          extraClass={extraClass}
          showWhy={showWhy}
          showSaved={showSaved}
        />
      ))}
    </>
  );
}

/**
 * A card is scanned, not read. The item name, source link, bid, and clock are the primary
 * path; the comparable and opportunity are secondary context that mobile can leave behind.
 */
export const LotCard = memo(function LotCard({ lot }: { lot: Lot }) {
  const now = useNow();
  const ended = hasEnded(lot.endsAt, now);
  const location = locationLabelOrEmpty(lot);
  return (
    <>
      <div className="bidrl-lot-card__media">
        <LotThumbLink lot={lot} className="bidrl-lot-card__img" />
        {lot.dealScore != null ? (
          <span className={`bidrl-gap bidrl-gap--${gapTone(lot.dealScore)}`}>
            {pct(lot.dealScore)} under
          </span>
        ) : null}
        {ended ? (
          <span className="bidrl-gap bidrl-gap--ended">Ended</span>
        ) : null}
        <FavoriteStar lot={lot} />
      </div>
      <div className="bidrl-lot-card__body">
        <h3 className="bidrl-lot-card__title">
          <LotLink lot={lot} />
        </h3>
        <div className="bidrl-lot-card__facts">
          <span className="bidrl-lot-card__where">
            {[location, lot.bidCount ? `${lot.bidCount} bids` : ""]
              .filter(Boolean)
              .join(" · ")}
          </span>
          <span className="bidrl-lot-card__when">
            {lot.endsAt ? <Countdown iso={lot.endsAt} /> : <Dash />}
          </span>
        </div>
        <div className="bidrl-lot-card__price">
          <span className="bidrl-lot-card__bid">
            {cents(lot.currentBidCents)}
          </span>
          <span className="bidrl-lot-card__bid-label">Current bid</span>
        </div>
      </div>
      {lot.favoriteNote ? (
        <p className="bidrl-note">{lot.favoriteNote}</p>
      ) : null}
      {lot.matchReason ? (
        <p className="bidrl-intent-reason">{lot.matchReason}</p>
      ) : null}
    </>
  );
});

function SimilarList({ lots }: { lots: Lot[] }) {
  return (
    <div className="bidrl-similar">
      {lots.map((lot) => (
        <div key={lot.id} className="bidrl-similar__row">
          <LotLink lot={lot}>
            {lot.lotCode || lot.title || lot.id}
          </LotLink>
          <span>
            {cents(lot.currentBidCents)}
            {lot.endsAt ? (
              <>
                {" · "}
                <Countdown iso={lot.endsAt} />
              </>
            ) : null}
          </span>
        </div>
      ))}
    </div>
  );
}

export function LotRefreshIndicator({ refreshing }: { refreshing: boolean }) {
  if (!refreshing) return null;
  return (
    <div className="bidrl-lot-refreshing" role="status" aria-live="polite">
      Updating results…
    </div>
  );
}

export function LotLoadMore({
  hasMore,
  loading,
  onLoadMore,
}: {
  hasMore: boolean;
  loading: boolean;
  onLoadMore: () => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const node = ref.current;
    if (
      !node ||
      !hasMore ||
      loading ||
      typeof IntersectionObserver === "undefined"
    )
      return;
    const observer = new IntersectionObserver(
      ([entry]) => {
        if (entry?.isIntersecting) onLoadMore();
      },
      { rootMargin: "800px" },
    );
    observer.observe(node);
    return () => observer.disconnect();
  }, [hasMore, loading, onLoadMore]);

  if (!hasMore && !loading) return null;
  return (
    <div ref={ref} className="bidrl-load-more" aria-live="polite">
      {loading ? <Hint>Loading more lots…</Hint> : null}
    </div>
  );
}

export function LotBrowser({
  lots,
  empty,
  view,
  groupSimilar = true,
  sort: controlledSort,
  onSort: controlledOnSort,
}: {
  lots: Lot[];
  empty: string;
  view: "grid" | "table";
  groupSimilar?: boolean;
  sort?: SortState<LotSortColumn> | null;
  onSort?: (column: LotSortColumn) => void;
}) {
  const local = useColumnSort<LotSortColumn>(LOT_SORT_DEFAULTS);
  const sort = controlledSort === undefined ? local.sort : controlledSort;
  const onSort = controlledOnSort ?? local.onSort;
  const groups = useMemo(() => {
    const grouped = groupSimilar
      ? groupSimilarLots(lots)
      : lots.map((lot) => ({
          key: `id:${lot.id}`,
          label: lot.title,
          lots: [lot],
        }));
    return sortLotGroups(grouped, sort);
  }, [lots, groupSimilar, sort, view]);
  const [open, setOpen] = useState<Record<string, boolean>>({});
  const toggle = (key: string) =>
    setOpen((prev) => ({ ...prev, [key]: !prev[key] }));
  const showWhy = lots.some((lot) => Boolean(lot.matchReason));
  // Only when every row is a saved lot — that is the Saved screen. A "Saved" column on
  // the catalog would be blank for almost every row and earn none of its width.
  const showSaved =
    lots.length > 0 && lots.every((lot) => Boolean(lot.savedAt));

  if (lots.length === 0) {
    return <EmptyState>{empty}</EmptyState>;
  }

  if (view === "grid") {
    return (
      <div className="bidrl-lot-grid bidrl-lot-grid--workspace">
        {groups.map((group) => (
          <LotGroupCard
            key={group.key}
            group={group}
            open={Boolean(open[group.key])}
            onToggle={() => toggle(group.key)}
          />
        ))}
      </div>
    );
  }

  return (
    <div className="data-table-wrap">
      <table className="data-table bidrl-lot-table bidrl-lot-table--workspace">
        <thead>
          <tr>
            <SortedHead column="name" sort={sort} onSort={onSort}>
              Lot
            </SortedHead>
            <SortedHead column="location" sort={sort} onSort={onSort}>
              Auction
            </SortedHead>
            <SortedHead column="bid" sort={sort} onSort={onSort}>
              Current bid
            </SortedHead>
            <th>Bids</th>
            <SortedHead column="ends" sort={sort} onSort={onSort}>
              Closes
            </SortedHead>
            <th>Status</th>
            {showWhy ? (
              <SortedHead column="why" sort={sort} onSort={onSort}>
                Why
              </SortedHead>
            ) : null}
            <th className="cc-table__actions">
              <span className="cc-sr-only">Open</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {groups.map((group) => (
            <LotGroupRows
              key={group.key}
              group={group}
              open={Boolean(open[group.key])}
              onToggle={() => toggle(group.key)}
              showWhy={showWhy}
              showSaved={showSaved}
            />
          ))}
        </tbody>
      </table>
    </div>
  );
}

function sameGroup(left: SimilarGroup, right: SimilarGroup): boolean {
  if (left.key !== right.key || left.lots.length !== right.lots.length)
    return false;
  return left.lots.every((lot, index) => lot === right.lots[index]);
}

export const LotGroupCard = memo(
  function LotGroupCard({
    group,
    open,
    onToggle,
  }: {
    group: SimilarGroup;
    open: boolean;
    onToggle: () => void;
  }) {
    const head = group.lots[0];
    if (!head) return null;
    const rest = group.lots.slice(1);
    return (
      <article className="bidrl-lot-card bidrl-lot-card--workspace">
        <LotCard lot={head} />
        {rest.length > 0 ? (
          <>
            <Button size="sm" pressed={open} onClick={onToggle}>
              {open ? "Hide similar" : `${rest.length} similar`}
            </Button>
            {open ? <SimilarList lots={rest} /> : null}
          </>
        ) : null}
      </article>
    );
  },
  (left, right) =>
    left.open === right.open && sameGroup(left.group, right.group),
);

export const LotGroupRows = memo(
  function LotGroupRows({
    group,
    open,
    onToggle,
    showWhy = false,
    showSaved = false,
  }: {
    group: SimilarGroup;
    open: boolean;
    onToggle: () => void;
    showWhy?: boolean;
    showSaved?: boolean;
  }) {
    const head = group.lots[0];
    if (!head) return null;
    const rest = group.lots.slice(1);
    return (
      <>
        <LotTableRow
          lot={head}
          showWhy={showWhy}
          showSaved={showSaved}
          similarCount={rest.length}
          similarOpen={open}
          onToggleSimilar={onToggle}
        />
        {open ? (
          <LotTableRows
            lots={rest}
            extraClass="bidrl-similar-row"
            showWhy={showWhy}
            showSaved={showSaved}
          />
        ) : null}
      </>
    );
  },
  (left, right) =>
    left.open === right.open &&
    left.showWhy === right.showWhy &&
    left.showSaved === right.showSaved &&
    sameGroup(left.group, right.group),
);
