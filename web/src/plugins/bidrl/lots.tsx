/** A list of lots, as cards or as a table, with near-identical lots folded together. */
import { memo, useEffect, useMemo, useRef, useState } from "react";
import {
  Badge,
  Button,
  Countdown,
  Dash,
  EmptyState,
  Hint,
  Link,
  Table,
  useNow,
} from "@cc/ui";
import {
  LOT_SORT_DEFAULTS,
  cents,
  comparableHint,
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
import { BidrlLink } from "./chrome";
import {
  FavoriteStar,
  LotThumbLink,
  LotLocation,
  LotTableTitle,
  LotComparable,
  bucketTone,
  savedOn,
} from "./lotparts";

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
  const gap =
    lot.dealScore == null ? "No comparable" : `${pct(lot.dealScore)} below`;
  const gapClass = `bidrl-table-gap__value bidrl-table-gap__value--${gapTone(lot.dealScore)}`;
  return (
    <tr className={extraClass}>
      <td className="bidrl-table-cell--save">
        <FavoriteStar lot={lot} />
      </td>
      <td className="bidrl-table-cell--item">
        <LotTableTitle lot={lot} />
        {similarCount > 0 ? (
          <Button size="sm" pressed={similarOpen} onClick={onToggleSimilar}>
            {similarOpen ? "Hide similar" : `${similarCount} similar`}
          </Button>
        ) : null}
      </td>
      <td className="bidrl-table-cell--bid">
        <strong>
          <span className="bidrl-table-cell__label">Current bid</span>
          {cents(lot.currentBidCents)}
        </strong>
        <Hint>
          {lot.bidCount
            ? `${lot.bidCount} bid${lot.bidCount === 1 ? "" : "s"}`
            : "No bids"}
        </Hint>
      </td>
      <td className="bidrl-table-cell--comp">
        <div className="bidrl-table-comp">
          <LotComparable lot={lot} />
        </div>
      </td>
      <td className="bidrl-table-cell--gap">
        <div className="bidrl-table-gap">
          <span className={gapClass}>{gap}</span>
          {lot.priceCents != null ? (
            <span className="bidrl-table-gap__comp">
              vs {cents(lot.priceCents)}
            </span>
          ) : null}
        </div>
      </td>
      <td className="bidrl-table-cell--ends">
        <span className="bidrl-table-cell__label">Time left</span>
        {lot.endsAt ? <Countdown iso={lot.endsAt} /> : <Dash />}
      </td>
      {showWhy ? (
        <td className="bidrl-table-cell--why">{lot.matchReason || <Dash />}</td>
      ) : null}
      {showSaved ? (
        <td className="bidrl-table-cell--saved">
          {savedOn(lot.savedAt) || <Dash />}
        </td>
      ) : null}
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
      <div className="bidrl-lot-card__title">
        <Link to={`/bidrl/lot/${encodeURIComponent(lot.id)}`}>
          {lot.title || lot.id}
        </Link>
        {(lot.identification && lot.identification !== lot.title) || lot.url ? (
          <div className="bidrl-lot-card__subline">
            {lot.identification && lot.identification !== lot.title ? (
              <span>{lot.identification}</span>
            ) : null}
            {lot.url ? (
              <BidrlLink
                href={lot.url}
                ariaLabel={`Open ${lot.title || lot.id} on BidRL`}
              >
                BidRL ↗
              </BidrlLink>
            ) : null}
          </div>
        ) : null}
      </div>
      <div className="bidrl-lot-card__price">
        <span className="bidrl-lot-card__bid">
          <span className="bidrl-lot-card__bid-label">Current bid</span>
          {cents(lot.currentBidCents)}
        </span>
        {lot.priceCents != null ? (
          <span className="bidrl-lot-card__comp">
            vs {cents(lot.priceCents)}
            {comparableHint(lot) ? ` ${comparableHint(lot)}` : ""}
          </span>
        ) : (
          <span className="bidrl-lot-card__comp" />
        )}
      </div>
      <div className="bidrl-lot-card__facts">
        <div className="bidrl-lot-card__when">
          <span className="bidrl-lot-card__when-label">Time left</span>
          {lot.endsAt ? <Countdown iso={lot.endsAt} /> : <Dash />}
        </div>
        <div className="bidrl-lot-card__where">
          {locationLabelOrEmpty(lot) ? <LotLocation lot={lot} /> : <Dash />}
        </div>
        {lot.category ? (
          <Badge>{lot.category}</Badge>
        ) : (
          <span className="bidrl-lot-card__chip-slot" />
        )}
        <Badge tone={bucketTone(lot.bucket)}>
          {lot.bucket.replace("_", " ")}
        </Badge>
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

export function SimilarList({ lots }: { lots: Lot[] }) {
  return (
    <div className="bidrl-similar">
      {lots.map((lot) => (
        <div key={lot.id} className="bidrl-similar__row">
          <Link to={`/bidrl/lot/${encodeURIComponent(lot.id)}`}>
            {lot.lotCode || lot.title || lot.id}
          </Link>
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
    // Similarity is useful for a visual grid, but hiding rows in a table makes exact
    // lot-by-lot comparison slower. Every table row is therefore always a real lot.
    const shouldGroup = groupSimilar && view === "grid";
    const grouped = shouldGroup
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
      <div className="bidrl-lot-grid">
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
    <Table
      className="bidrl-lot-table"
      head={
        <>
          <th>
            <span className="cc-sr-only">Saved</span>
          </th>
          <SortedHead column="name" sort={sort} onSort={onSort}>
            Item
          </SortedHead>
          <SortedHead column="bid" sort={sort} onSort={onSort} numeric>
            Bid
          </SortedHead>
          <SortedHead column="price" sort={sort} onSort={onSort} numeric>
            Comparable
          </SortedHead>
          <SortedHead column="gap" sort={sort} onSort={onSort} numeric>
            Opportunity
          </SortedHead>
          <SortedHead column="ends" sort={sort} onSort={onSort}>
            Closes
          </SortedHead>
          {showWhy ? (
            <SortedHead column="why" sort={sort} onSort={onSort}>
              Why
            </SortedHead>
          ) : null}
          {showSaved ? (
            <SortedHead column="saved" sort={sort} onSort={onSort}>
              Saved
            </SortedHead>
          ) : null}
        </>
      }
    >
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
    </Table>
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
      <article className="bidrl-lot-card">
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
