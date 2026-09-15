/** A list of lots, as cards or as a table, with near-identical lots folded together. */
import {
  memo,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
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
} from "./model";
import { useColumnSort, SortedHead } from "./sorting";
import { FavoriteStar, LotThumbLink, LotTitle, LotLocation, LotComparable, bucketTone, savedOn } from "./lotparts";

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
        <tr key={lot.id} className={extraClass}>
          <td className="bidrl-col-star"><FavoriteStar lot={lot} /></td>
          <td><LotThumbLink lot={lot} /></td>
          <td className="cc-nowrap">{lot.lotCode || <Dash />}</td>
          <td><LotTitle lot={lot} showLotCode={false} /></td>
          <td>{cents(lot.currentBidCents)}</td>
          <td>{lot.endsAt ? <Countdown iso={lot.endsAt} /> : <Dash />}</td>
          <td className="bidrl-col-location">{locationLabelOrEmpty(lot) || <Dash />}</td>
          <td>{lot.category ? <Badge>{lot.category}</Badge> : <Dash />}</td>
          <td><LotComparable lot={lot} /></td>
          <td className="cc-num">{lot.dealScore != null ? pct(lot.dealScore) : <Dash />}</td>
          <td><Badge tone={bucketTone(lot.bucket)}>{lot.bucket.replace("_", " ")}</Badge></td>
          {showWhy ? <td>{lot.matchReason || <Dash />}</td> : null}
          {showSaved ? <td className="cc-nowrap">{savedOn(lot.savedAt) || <Dash />}</td> : null}
        </tr>
      ))}
    </>
  );
}

/**
 * A card is scanned, not read. The photograph carries the gap — the one number the whole
 * plugin exists to produce — and the bid sits under it as the figure you would act on;
 * everything else is secondary text below the fold of the eye.
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
        {ended ? <span className="bidrl-gap bidrl-gap--ended">Ended</span> : null}
        <FavoriteStar lot={lot} />
      </div>
      <div className="bidrl-lot-card__title"><LotTitle lot={lot} /></div>
      <div className="bidrl-lot-card__price">
        <span className="bidrl-lot-card__bid">{cents(lot.currentBidCents)}</span>
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
          {lot.endsAt ? <Countdown iso={lot.endsAt} /> : <Dash />}
        </div>
        <div className="bidrl-lot-card__where">
          {locationLabelOrEmpty(lot) ? <LotLocation lot={lot} /> : <Dash />}
        </div>
        {lot.category ? <Badge>{lot.category}</Badge> : <span className="bidrl-lot-card__chip-slot" />}
        <Badge tone={bucketTone(lot.bucket)}>{lot.bucket.replace("_", " ")}</Badge>
      </div>
      {lot.favoriteNote ? <p className="bidrl-note">{lot.favoriteNote}</p> : null}
      {lot.matchReason ? <p className="bidrl-intent-reason">{lot.matchReason}</p> : null}
    </>
  );
});

export function SimilarList({ lots }: { lots: Lot[] }) {
  return (
    <div className="bidrl-similar">
      {lots.map((lot) => (
        <div key={lot.id} className="bidrl-similar__row">
          <Link to={`/bidrl/lot/${encodeURIComponent(lot.id)}`}>{lot.lotCode || lot.title || lot.id}</Link>
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
    if (!node || !hasMore || loading || typeof IntersectionObserver === "undefined") return;
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
}: {
  lots: Lot[];
  empty: string;
  view: "grid" | "table";
  groupSimilar?: boolean;
}) {
  const { sort, onSort } = useColumnSort<LotSortColumn>(LOT_SORT_DEFAULTS);
  const groups = useMemo(() => {
    const grouped = groupSimilar
      ? groupSimilarLots(lots)
      : lots.map((lot) => ({ key: `id:${lot.id}`, label: lot.title, lots: [lot] }));
    return sortLotGroups(grouped, sort);
  }, [lots, groupSimilar, sort]);
  const [open, setOpen] = useState<Record<string, boolean>>({});
  const toggle = (key: string) => setOpen((prev) => ({ ...prev, [key]: !prev[key] }));
  const showWhy = lots.some((lot) => Boolean(lot.matchReason));
  // Only when every row is a saved lot — that is the Saved screen. A "Saved" column on
  // the catalog would be blank for almost every row and earn none of its width.
  const showSaved = lots.length > 0 && lots.every((lot) => Boolean(lot.savedAt));

  if (lots.length === 0) {
    return <EmptyState>{empty}</EmptyState>;
  }

  if (view === "grid") {
    return (
      <div className="bidrl-lot-grid">
        {groups.map((group) => (
          <LotGroupCard key={group.key} group={group} open={Boolean(open[group.key])} onToggle={() => toggle(group.key)} />
        ))}
      </div>
    );
  }

  return (
    <Table
      className="bidrl-lot-table"
      head={
        <>
          <th><span className="cc-sr-only">Saved</span></th>
          <th></th>
          <SortedHead column="lot" sort={sort} onSort={onSort}>Lot</SortedHead>
          <SortedHead column="name" sort={sort} onSort={onSort}>Name</SortedHead>
          <SortedHead column="bid" sort={sort} onSort={onSort}>Bid</SortedHead>
          <SortedHead column="ends" sort={sort} onSort={onSort}>Ends</SortedHead>
          <SortedHead column="location" sort={sort} onSort={onSort}>Location</SortedHead>
          <SortedHead column="category" sort={sort} onSort={onSort}>Category</SortedHead>
          <SortedHead column="price" sort={sort} onSort={onSort}>Price</SortedHead>
          <SortedHead column="gap" sort={sort} onSort={onSort} numeric>Gap</SortedHead>
          <SortedHead column="bucket" sort={sort} onSort={onSort}>Bucket</SortedHead>
          {showWhy ? <SortedHead column="why" sort={sort} onSort={onSort}>Why</SortedHead> : null}
          {showSaved ? <SortedHead column="saved" sort={sort} onSort={onSort}>Saved</SortedHead> : null}
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
  if (left.key !== right.key || left.lots.length !== right.lots.length) return false;
  return left.lots.every((lot, index) => lot === right.lots[index]);
}

export const LotGroupCard = memo(function LotGroupCard({
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
}, (left, right) => left.open === right.open && sameGroup(left.group, right.group));

export const LotGroupRows = memo(function LotGroupRows({
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
      <tr>
        <td className="bidrl-col-star"><FavoriteStar lot={head} /></td>
        <td><LotThumbLink lot={head} /></td>
        <td className="cc-nowrap">{head.lotCode || <Dash />}</td>
        <td>
          <LotTitle lot={head} showLotCode={false} />
          {rest.length > 0 ? (
            <div>
              <Button size="sm" pressed={open} onClick={onToggle}>
                {open ? "Hide similar" : `${rest.length} similar`}
              </Button>
            </div>
          ) : null}
        </td>
        <td>{cents(head.currentBidCents)}</td>
        <td>{head.endsAt ? <Countdown iso={head.endsAt} /> : <Dash />}</td>
        <td className="bidrl-col-location">{locationLabelOrEmpty(head) || <Dash />}</td>
        <td>{head.category ? <Badge>{head.category}</Badge> : <Dash />}</td>
        <td><LotComparable lot={head} /></td>
        <td className="cc-num">{head.dealScore != null ? pct(head.dealScore) : <Dash />}</td>
        <td><Badge tone={bucketTone(head.bucket)}>{head.bucket.replace("_", " ")}</Badge></td>
        {showWhy ? <td>{head.matchReason || <Dash />}</td> : null}
        {showSaved ? <td className="cc-nowrap">{savedOn(head.savedAt) || <Dash />}</td> : null}
      </tr>
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
}, (left, right) =>
  left.open === right.open &&
  left.showWhy === right.showWhy &&
  left.showSaved === right.showSaved &&
  sameGroup(left.group, right.group),
);
