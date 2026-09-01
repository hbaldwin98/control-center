/**
 * BIDRL — score auction lots from photographs, not titles.
 *
 * Imports `@cc/ui` and this directory only. Auction and lot ids come from the path so
 * this module never imports the shell router. One sidebar item; Feed / Auctions / Lots
 * are in-plugin tabs.
 */
import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import {
  ActionsHeader,
  Badge,
  Button,
  Callout,
  Card,
  Checkbox,
  Countdown,
  Dash,
  EmptyState,
  Field,
  Grid,
  Hint,
  Input,
  Link,
  Loading,
  Metric,
  Page,
  PageHeader,
  PluginAIHint,
  PluginDisabledError,
  RelativeTime,
  Row,
  Select,
  SortHeader,
  Stack,
  Table,
  Tabs,
  Textarea,
  Toolbar,
  pluginApi,
  useNavigate,
  usePath,
  useNow,
  useQueryState,
  useRouteParams,
  useSnapshot,
} from "@cc/ui";
import type { PluginModule, PluginSurfaceProps, UseSnapshotResult } from "@cc/ui";
import {
  AUCTION_SORT_DEFAULTS,
  INTENT_EXAMPLES,
  LOT_CATEGORIES,
  LOT_PRESETS,
  LOT_SORT_DEFAULTS,
  SITES_SORT_DEFAULTS,
  affiliateParam,
  cents,
  cleanupMessage,
  comparableHint,
  cycleSort,
  eventBoundary,
  filterLabel,
  gapTone,
  groupByLocation,
  groupSimilarLots,
  hasEnded,
  locationLabel,
  locationLabelOrEmpty,
  lotNeighbours,
  parseAffiliateParam,
  pct,
  sortAuctions,
  sortLotGroups,
  sortSitesAuctions,
  type Auction,
  type AuctionPage,
  type AuctionSortColumn,
  type AuctionIndex,
  type AuctionsPage,
  type CleanupResult,
  type FeedPage,
  type IntentPage,
  type LocationGroup,
  type LocationsPage,
  type Lot,
  type LotSortColumn,
  type LotsPage,
  type OverviewPage,
  type SimilarGroup,
  type SitesAuction,
  type SitesPage,
  type SitesSortColumn,
  type SortDir,
  type SortState,
} from "./model";
import "./index.css";

const api = pluginApi("bidrl");

function useFeed(filter: string, q: string): UseSnapshotResult<FeedPage> {
  const load = useCallback(async (signal: AbortSignal) => {
    const params = new URLSearchParams();
    if (filter) params.set("filter", filter);
    if (q.trim()) params.set("q", q.trim());
    const qs = params.toString();
    const data = await api.get<FeedPage>(`/feed${qs ? `?${qs}` : ""}`, signal);
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, [filter, q]);
  return useSnapshot(load, { events: "bidrl.**" });
}

function useAuctions(): UseSnapshotResult<AuctionsPage> {
  const load = useCallback(async (signal: AbortSignal) => {
    const data = await api.get<AuctionsPage>("/auctions", signal);
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, []);
  return useSnapshot(load, { events: "bidrl.**" });
}

function useAuction(id: string): UseSnapshotResult<AuctionPage> {
  const load = useCallback(async (signal: AbortSignal) => {
    const data = await api.get<AuctionPage>(`/auctions/${encodeURIComponent(id)}`, signal);
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, [id]);
  return useSnapshot(load, { events: "bidrl.**" });
}

function useLot(id: string): UseSnapshotResult<Lot> {
  const load = useCallback(async (signal: AbortSignal) => {
    const data = await api.get<Lot>(`/lots/${encodeURIComponent(id)}`, signal);
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, [id]);
  return useSnapshot(load, { events: "bidrl.**" });
}

function useSites(): UseSnapshotResult<SitesPage> {
  const load = useCallback(async (signal: AbortSignal) => {
    const data = await api.get<SitesPage>("/sites/auctions", signal);
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, []);
  return useSnapshot(load, { events: "bidrl.**" });
}

function useLots(
  filter: string,
  q: string,
  bucket: string,
  category: string,
  ending: string,
  affiliate: string,
): UseSnapshotResult<LotsPage> {
  const load = useCallback(async (signal: AbortSignal) => {
    const params = new URLSearchParams();
    if (filter) params.set("filter", filter);
    if (q.trim()) params.set("q", q.trim());
    if (bucket && bucket !== "all") params.set("bucket", bucket);
    if (category && category !== "all") params.set("category", category);
    if (ending) params.set("ending", ending);
    if (affiliate) params.set("affiliate", affiliate);
    const qs = params.toString();
    const data = await api.get<LotsPage>(`/lots${qs ? `?${qs}` : ""}`, signal);
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, [filter, q, bucket, category, ending, affiliate]);
  return useSnapshot(load, { events: "bidrl.**" });
}

/**
 * The locations you have lots at. Its own request, not derived from the lot list:
 * deriving it would delete every unselected location from the filter the moment
 * you picked one.
 */
function useLocations(): UseSnapshotResult<LocationsPage> {
  const load = useCallback(async (signal: AbortSignal) => {
    const data = await api.get<LocationsPage>("/locations", signal);
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, []);
  return useSnapshot(load, { events: "bidrl.**" });
}

/**
 * A lot's siblings, for paging through an auction without going back to a list. This is
 * the index endpoint, not the auction page: three pieces of navigation are not worth
 * downloading every full lot row of a large auction. An empty id resolves to nothing
 * rather than fetching, so the lot screen can call this before its own record arrives.
 */
function useAuctionLots(auctionId: string): AuctionIndex {
  const load = useCallback(async (signal: AbortSignal) => {
    if (!auctionId) return { data: { title: "", lots: [] }, asOfEventId: eventBoundary(0) };
    const data = await api.get<AuctionIndex & { latestEventId: number }>(
      `/auctions/${encodeURIComponent(auctionId)}/index`,
      signal,
    );
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, [auctionId]);
  const snap = useSnapshot<AuctionIndex>(load, { events: "bidrl.**" });
  return snap.status === "ready" ? snap.data : { title: "", lots: [] };
}

function useOverview(): UseSnapshotResult<OverviewPage> {
  const load = useCallback(async (signal: AbortSignal) => {
    const data = await api.get<OverviewPage>("/overview", signal);
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, []);
  return useSnapshot(load, { events: "bidrl.**" });
}

function useIntent(): UseSnapshotResult<IntentPage> {
  const load = useCallback(async (signal: AbortSignal) => {
    const data = await api.get<IntentPage>("/intent", signal);
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, []);
  return useSnapshot(load, { events: "bidrl.**" });
}

function useLotView(): ["grid" | "table", (view: "grid" | "table") => void] {
  const [view, setView] = useState<"grid" | "table">(() => {
    try {
      return localStorage.getItem("bidrl.lotView") === "table" ? "table" : "grid";
    } catch {
      return "grid";
    }
  });
  const change = (next: "grid" | "table") => {
    setView(next);
    try {
      localStorage.setItem("bidrl.lotView", next);
    } catch {
      /* ignore quota / private mode */
    }
  };
  return [view, change];
}

/**
 * Grid and table are two views of one list, not two actions, so they sit in a single
 * joined control where the pressed half reads as the current view.
 */
function ViewToggle({ value, onChange }: { value: "grid" | "table"; onChange: (view: "grid" | "table") => void }) {
  return (
    <div className="bidrl-seg" role="group" aria-label="Lot view">
      <Button size="sm" pressed={value === "grid"} onClick={() => onChange("grid")}>
        Grid
      </Button>
      <Button size="sm" pressed={value === "table"} onClick={() => onChange("table")}>
        Table
      </Button>
    </div>
  );
}

/**
 * Every button here queues a job rather than doing the work, so every button owes the
 * same answer: what was queued, under which job number, and where to watch it. Screens
 * used to post and say nothing, which read as a dead button.
 */
function useAction() {
  const [busy, setBusy] = useState<string | null>(null);
  const [notice, setNotice] = useState<ReactNode | null>(null);
  const [error, setError] = useState<string | null>(null);
  const run = async (key: string, label: string, call: () => Promise<{ jobId?: number } | void>) => {
    setBusy(key);
    setError(null);
    setNotice(null);
    try {
      const result = (await call()) ?? {};
      setNotice(
        result.jobId != null ? (
          <>
            {label} queued as <Link to="/jobs">job {result.jobId}</Link>. This page updates as it lands.
          </>
        ) : (
          `${label} done.`
        ),
      );
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  };
  return { busy, notice, error, run, setNotice, setError };
}

function bucketTone(bucket: string): "neutral" | "ok" | "warn" | "danger" {
  if (bucket === "priced") return "ok";
  if (bucket === "worth_opening" || bucket === "research") return "warn";
  if (bucket === "discarded" || bucket === "rejected") return "danger";
  return "neutral";
}

function useColumnSort<C extends string>(defaults: Record<C, SortDir>) {
  const [sort, setSort] = useState<SortState<C> | null>(null);
  const onSort = (column: C) => setSort((cur) => cycleSort(cur, column, defaults[column]));
  return { sort, onSort };
}

function SortedHead<C extends string>({
  column,
  sort,
  onSort,
  numeric,
  children,
}: {
  column: C;
  sort: SortState<C> | null;
  onSort: (column: C) => void;
  numeric?: boolean;
  children: string;
}) {
  return (
    <SortHeader
      active={sort?.column === column}
      direction={sort?.dir ?? "asc"}
      numeric={numeric}
      onClick={() => onSort(column)}
    >
      {children}
    </SortHeader>
  );
}

function BidrlLink({ href, children }: { href: string; children?: string }) {
  if (!href) return null;
  return (
    <a href={href} target="_blank" rel="noreferrer">
      {children ?? "Open on BidRL"}
    </a>
  );
}

function LocationSections<T>({
  groups,
  empty,
  children,
}: {
  groups: LocationGroup<T>[];
  empty: string;
  children: (items: T[]) => ReactNode;
}) {
  if (groups.length === 0) {
    return <EmptyState>{empty}</EmptyState>;
  }
  return (
    <div className="bidrl-locations">
      {groups.map((group, i) => (
        <details key={group.key} className="bidrl-location" open={i === 0 || groups.length <= 3}>
          <summary>
            <span className="bidrl-location__name">{group.label}</span>
            <span className="bidrl-location__meta">
              {group.items.length} auction{group.items.length === 1 ? "" : "s"}
            </span>
          </summary>
          <div className="bidrl-location__body">{children(group.items)}</div>
        </details>
      ))}
    </div>
  );
}

function CollectedTable({ auctions }: { auctions: Auction[] }) {
  const { sort, onSort } = useColumnSort<AuctionSortColumn>(AUCTION_SORT_DEFAULTS);
  const rows = useMemo(() => sortAuctions(auctions, sort), [auctions, sort]);
  return (
    <Table
      head={
        <>
          <SortedHead column="title" sort={sort} onSort={onSort}>Auction</SortedHead>
          <SortedHead column="status" sort={sort} onSort={onSort}>Status</SortedHead>
          <SortedHead column="lots" sort={sort} onSort={onSort} numeric>Lots</SortedHead>
          <SortedHead column="ends" sort={sort} onSort={onSort}>Ends</SortedHead>
        </>
      }
    >
      {rows.map((a) => (
        <tr key={a.id}>
          <td>
            <Link to={`/bidrl/auction/${encodeURIComponent(a.id)}`}>{a.title || a.id}</Link>
            {a.url ? <Hint><BidrlLink href={a.url} /></Hint> : null}
          </td>
          <td><Badge>{a.status}</Badge></td>
          <td className="cc-num">{a.lotCount}</td>
          <td>{a.endsAt ? <Countdown iso={a.endsAt} /> : <Dash />}</td>
        </tr>
      ))}
    </Table>
  );
}

function SitesTable({
  auctions,
  disabled,
  busy,
  onCollect,
}: {
  auctions: SitesAuction[];
  disabled: boolean;
  busy: string | null;
  onCollect: (url: string) => void;
}) {
  const { sort, onSort } = useColumnSort<SitesSortColumn>(SITES_SORT_DEFAULTS);
  const rows = useMemo(() => sortSitesAuctions(auctions, sort), [auctions, sort]);
  return (
    <Table
      head={
        <>
          <SortedHead column="title" sort={sort} onSort={onSort}>Auction</SortedHead>
          <SortedHead column="lots" sort={sort} onSort={onSort} numeric>Lots</SortedHead>
          <SortedHead column="ends" sort={sort} onSort={onSort}>Ends</SortedHead>
          <ActionsHeader label="Collect" />
        </>
      }
    >
      {rows.map((a) => (
        <tr key={a.id}>
          <td>
            {a.collected ? <Link to={`/bidrl/auction/${encodeURIComponent(a.id)}`}>{a.title}</Link> : a.title}
            {a.url ? (
              <Hint><BidrlLink href={a.url} /></Hint>
            ) : null}
          </td>
          <td className="cc-num">{a.itemCount}</td>
          <td>{a.endsAt ? <Countdown iso={a.endsAt} /> : <Dash />}</td>
          <td>
            {a.collected ? "Collected" : (
              <Button size="sm" disabled={disabled || busy !== null} onClick={() => onCollect(a.url)}>
                Collect
              </Button>
            )}
          </td>
        </tr>
      ))}
    </Table>
  );
}

/**
 * The four sections, and which one a detail screen belongs to: a lot page is still the
 * catalog, an auction page is still Auctions, so the tab strip never goes blank under a
 * record you drilled into.
 */
const BIDRL_TABS = [
  { to: "/bidrl", label: "Overview", owns: (path: string) => path === "/bidrl" },
  {
    to: "/bidrl/auctions",
    label: "Auctions",
    owns: (path: string) => path === "/bidrl/auctions" || path.startsWith("/bidrl/auction/"),
  },
  {
    to: "/bidrl/lots",
    label: "Lots",
    owns: (path: string) => path === "/bidrl/lots" || path.startsWith("/bidrl/lot/"),
  },
  { to: "/bidrl/intent", label: "Intent", owns: (path: string) => path === "/bidrl/intent" },
] as const;

function BidrlTabs() {
  const path = usePath().replace(/\/+$/, "") || "/";
  return (
    <Tabs label="BIDRL sections">
      {BIDRL_TABS.map((item) => (
        <Link key={item.to} to={item.to} aria-current={item.owns(path) ? "page" : undefined}>
          {item.label}
        </Link>
      ))}
    </Tabs>
  );
}

function LotThumb({ lot, className }: { lot: Lot; className?: string | undefined }) {
  if (!lot.thumbUrl) {
    /* A box, not a dash: a missing photo must not shorten the row it sits in. */
    return <div className={className ? `${className}-empty` : "cc-lot-thumb bidrl-thumb-empty"}><Dash /></div>;
  }
  return <img className={className ?? "cc-lot-thumb"} src={lot.thumbUrl} alt="" width={className ? 220 : 48} height={className ? 220 : 48} />;
}

function LotThumbLink({ lot, className }: { lot: Lot; className?: string | undefined }) {
  const thumb = <LotThumb lot={lot} className={className} />;
  if (!lot.thumbUrl) return thumb;
  return (
    <Link
      to={`/bidrl/lot/${encodeURIComponent(lot.id)}`}
      className={className ? `${className}-link` : undefined}
      aria-label={`Open ${lot.title || lot.id}`}
    >
      {thumb}
    </Link>
  );
}

function LotPhotos({ urls }: { urls: string[] }) {
  const count = urls.length;
  const [index, setIndex] = useState(0);
  const [expanded, setExpanded] = useState(false);
  const current = urls[Math.min(index, Math.max(count - 1, 0))] ?? "";

  const step = useCallback(
    (delta: number) => {
      if (count < 2) return;
      setIndex((i) => (i + delta + count) % count);
    },
    [count],
  );

  useEffect(() => {
    if (count < 2) return;
    const next = new Image();
    next.src = urls[(index + 1) % count] ?? "";
    const prev = new Image();
    prev.src = urls[(index - 1 + count) % count] ?? "";
  }, [count, index, urls]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const tag = (e.target as HTMLElement | null)?.tagName;
      if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return;
      if (e.key === "ArrowRight") {
        e.preventDefault();
        step(1);
      } else if (e.key === "ArrowLeft") {
        e.preventDefault();
        step(-1);
      } else if (e.key === "Escape") {
        setExpanded(false);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [step]);

  useEffect(() => {
    document.querySelector(`[data-bidrl-thumb="${index}"]`)?.scrollIntoView({
      inline: "nearest",
      block: "nearest",
    });
  }, [index]);

  useEffect(() => {
    if (!expanded) return;
    const scroller = document.querySelector(".cc-main");
    if (!(scroller instanceof HTMLElement)) return;
    const prev = scroller.style.overflowY;
    scroller.style.overflowY = "hidden";
    return () => {
      scroller.style.overflowY = prev;
    };
  }, [expanded]);

  if (count === 0) return null;

  const stage = (
    <div className="bidrl-gallery__stage">
      {count > 1 ? (
        <Button
          size="sm"
          className="bidrl-gallery__nav bidrl-gallery__nav--prev"
          onClick={() => step(-1)}
          aria-label="Previous photo"
        >
          Prev
        </Button>
      ) : null}
      <button
        type="button"
        className="bidrl-gallery__frame"
        onClick={() => setExpanded(true)}
        aria-label={`Photo ${index + 1} of ${count}. Enlarge`}
      >
        <img src={current} alt="" />
      </button>
      {count > 1 ? (
        <Button
          size="sm"
          className="bidrl-gallery__nav bidrl-gallery__nav--next"
          onClick={() => step(1)}
          aria-label="Next photo"
        >
          Next
        </Button>
      ) : null}
    </div>
  );

  return (
    <>
      <Card
        title="Photos"
        actions={<Hint>{index + 1} / {count}</Hint>}
      >
        <div className="bidrl-gallery">
          {stage}
          {count > 1 ? (
            <div className="bidrl-gallery__thumbs" role="list">
              {urls.map((src, i) => (
                <button
                  key={src}
                  type="button"
                  role="listitem"
                  data-bidrl-thumb={i}
                  className="bidrl-gallery__thumb"
                  aria-current={i === index ? "true" : undefined}
                  aria-label={`Photo ${i + 1}`}
                  onClick={() => setIndex(i)}
                >
                  <img src={src} alt="" />
                </button>
              ))}
            </div>
          ) : null}
          <Hint>
            {count > 1 ? "Arrow keys move. " : ""}
            Click the photo to enlarge.
          </Hint>
        </div>
      </Card>
      {expanded ? (
        <div
          className="bidrl-lightbox"
          role="dialog"
          aria-modal="true"
          aria-label={`Photo ${index + 1} of ${count}`}
          onClick={() => setExpanded(false)}
        >
          <div className="bidrl-lightbox__bar" onClick={(e) => e.stopPropagation()}>
            <span>{index + 1} / {count}</span>
            <Button size="sm" onClick={() => setExpanded(false)}>
              Close
            </Button>
          </div>
          {count > 1 ? (
            <Button
              size="sm"
              className="bidrl-lightbox__nav bidrl-lightbox__nav--prev"
              onClick={(e) => {
                e.stopPropagation();
                step(-1);
              }}
              aria-label="Previous photo"
            >
              Prev
            </Button>
          ) : null}
          <img
            src={current}
            alt=""
            onClick={(e) => {
              e.stopPropagation();
              if (count > 1) step(1);
            }}
          />
          {count > 1 ? (
            <Button
              size="sm"
              className="bidrl-lightbox__nav bidrl-lightbox__nav--next"
              onClick={(e) => {
                e.stopPropagation();
                step(1);
              }}
              aria-label="Next photo"
            >
              Next
            </Button>
          ) : null}
        </div>
      ) : null}
    </>
  );
}

function LotMeta({ lot }: { lot: Lot }) {
  return (
    <>
      {lot.category ? <Badge>{lot.category}</Badge> : null}
      <Badge tone={bucketTone(lot.bucket)}>{lot.bucket.replace("_", " ")}</Badge>
    </>
  );
}

/**
 * Where the lot is. Shown on every card and row, and kept on narrow screens, because
 * ruling something out on the drive rather than on the price is the whole reason to
 * put it here — doing that from a phone is the common case, not the edge one.
 */
function LotLocation({ lot }: { lot: Lot }) {
  const label = locationLabelOrEmpty(lot);
  if (!label) return null;
  return <span className="bidrl-lot-location" title={`Auction location: ${label}`}>{label}</span>;
}

function LotTitle({ lot, showLotCode = true }: { lot: Lot; showLotCode?: boolean }) {
  const ident = lot.identification && lot.identification !== lot.title ? lot.identification : "";
  const hint = ident || (showLotCode ? lot.lotCode : "");
  return (
    <>
      <Link to={`/bidrl/lot/${encodeURIComponent(lot.id)}`}>{lot.title || lot.id}</Link>
      {hint || lot.url ? (
        <Hint>
          {hint}
          {lot.url ? (
            <>
              {hint ? " · " : ""}
              <BidrlLink href={lot.url} />
            </>
          ) : null}
        </Hint>
      ) : null}
    </>
  );
}

function LotComparable({ lot }: { lot: Lot }) {
  if (lot.priceCents == null) return <Dash />;
  return (
    <>
      {cents(lot.priceCents)}
      <Hint>
        {lot.sourceUrl ? (
          <a href={lot.sourceUrl} target="_blank" rel="noreferrer">
            {comparableHint(lot) || lot.sourceTitle || "source"}
          </a>
        ) : (
          comparableHint(lot) || <Dash />
        )}
      </Hint>
    </>
  );
}

function LotTableRows({ lots, extraClass, showWhy = false }: { lots: Lot[]; extraClass?: string; showWhy?: boolean }) {
  return (
    <>
      {lots.map((lot) => (
        <tr key={lot.id} className={extraClass}>
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
function LotCard({ lot }: { lot: Lot }) {
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
      </div>
      <div className="bidrl-lot-card__title"><LotTitle lot={lot} /></div>
      <div className="bidrl-lot-card__price">
        <span className="bidrl-lot-card__bid">{cents(lot.currentBidCents)}</span>
        {lot.priceCents != null ? (
          <span className="bidrl-lot-card__comp">
            vs {cents(lot.priceCents)}
            {comparableHint(lot) ? ` ${comparableHint(lot)}` : ""}
          </span>
        ) : null}
      </div>
      <div className="bidrl-lot-card__meta">
        {lot.endsAt ? <Countdown iso={lot.endsAt} /> : <Dash />}
        <LotLocation lot={lot} />
        <LotMeta lot={lot} />
      </div>
      {lot.matchReason ? <p className="bidrl-intent-reason">{lot.matchReason}</p> : null}
    </>
  );
}

function SimilarList({ lots }: { lots: Lot[] }) {
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

function LotBrowser({
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
        />
      ))}
    </Table>
  );
}

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
}

function LotGroupRows({
  group,
  open,
  onToggle,
  showWhy = false,
}: {
  group: SimilarGroup;
  open: boolean;
  onToggle: () => void;
  showWhy?: boolean;
}) {
  const head = group.lots[0];
  if (!head) return null;
  const rest = group.lots.slice(1);
  return (
    <>
      <tr>
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
      </tr>
      {open ? <LotTableRows lots={rest} extraClass="bidrl-similar-row" showWhy={showWhy} /> : null}
    </>
  );
}

function Notices({
  message,
  error,
  disabled,
}: {
  message: ReactNode | null;
  error: string | null;
  disabled: boolean;
}) {
  return (
    <>
      {message ? <Callout tone="ok">{message}</Callout> : null}
      {error ? <Callout tone="danger">{error}</Callout> : null}
      {disabled ? (
        <Callout>
          BIDRL is disabled. Enable it on the <Link to="/plugins/bidrl/settings">plugin screen</Link>.
        </Callout>
      ) : null}
      <PluginAIHint pluginId="bidrl" />
    </>
  );
}

/**
 * The front door. It answers "is there anything to look at" without being a fourth
 * listing of the same table: a few counts, the widest gaps, what closes next, and a link
 * into the catalog for each. Everything here is a shortcut to a filtered catalog URL.
 */
function Overview() {
  const snap = useOverview();
  const disabled = snap.error instanceof PluginDisabledError;
  const stats = snap.status === "ready" ? snap.data.stats : null;
  const deals = snap.status === "ready" ? snap.data.deals : [];
  const closing = snap.status === "ready" ? snap.data.closing : [];

  return (
    <Page>
      <PageHeader
        title="BIDRL"
        lede="Lots scored from photographs. A comparable appears only when a model or barcode is read and a search hit writes a dollar amount — eBay sold listings first."
      />
      <Stack>
        <BidrlTabs />
        <Notices message={null} error={null} disabled={disabled} />
        {snap.status === "loading" ? <Loading label="Loading…" /> : null}
        {snap.status === "error" && !disabled ? <Callout tone="danger">{snap.error.message}</Callout> : null}
        {stats != null && stats.lots === 0 ? (
          <Card title="Start here">
            <Stack>
              <Hint>
                Nothing is collected yet. Collect an auction, then scan it — scanning is what reads
                the photographs and produces comparables.
              </Hint>
              <div className="bidrl-actions">
                <Link to="/bidrl/auctions" className="cc-button cc-button--primary">
                  Collect an auction
                </Link>
              </div>
            </Stack>
          </Card>
        ) : null}
        {stats != null && stats.lots > 0 ? (
          <>
            <Grid density="metric">
              <StatLink to="/bidrl/auctions" label="Auctions" value={String(stats.auctions)} />
              <StatLink to="/bidrl/lots" label="Lots" value={String(stats.lots)} hint={`${stats.live} still open`} />
              <StatLink
                to="/bidrl/lots?filter=deals"
                label="Priced"
                value={String(stats.priced)}
                hint={stats.unscanned > 0 ? `${stats.unscanned} never scanned` : "all scanned"}
                tone={stats.priced > 0 ? "ok" : "neutral"}
              />
              <StatLink
                to="/bidrl/lots?ending=soon"
                label="Ending in 24h"
                value={String(stats.ending)}
                tone={stats.ending > 0 ? "warn" : "neutral"}
              />
            </Grid>
            <Card
              title="Widest gaps"
              actions={<Link to="/bidrl/lots?filter=deals">See all priced lots</Link>}
            >
              <LotBrowser
                lots={deals}
                empty="No lot has a comparable yet. Scan an auction, then reprice a lot whose photos show a model."
                view="grid"
                groupSimilar={false}
              />
            </Card>
            <Card title="Closing next" actions={<Link to="/bidrl/lots?ending=soon">See everything ending</Link>}>
              <LotBrowser
                lots={closing}
                empty="Nothing collected closes in the next week."
                view="table"
                groupSimilar={false}
              />
            </Card>
          </>
        ) : null}
      </Stack>
    </Page>
  );
}

/** A metric that is also the way in to the list it counts. */
function StatLink({
  to,
  label,
  value,
  hint,
  tone,
}: {
  to: string;
  label: string;
  value: string;
  hint?: string | undefined;
  tone?: "neutral" | "ok" | "warn" | "danger" | undefined;
}) {
  return (
    <Link to={to} className="bidrl-stat">
      <Metric label={label} value={value} hint={hint} tone={tone ?? "neutral"} />
    </Link>
  );
}

function Auctions() {
  const auctions = useAuctions();
  const sites = useSites();
  const [url, setUrl] = useState("");
  const { busy, notice, error, run, setNotice, setError } = useAction();
  const [cleaning, setCleaning] = useState(false);
  const disabled = auctions.error instanceof PluginDisabledError;
  const collected = auctions.status === "ready" ? auctions.data.auctions : [];
  const collectedGroups = useMemo(() => groupByLocation(collected), [collected]);
  const siteList = sites.status === "ready" ? sites.data.auctions : [];
  const siteGroups = useMemo(() => groupByLocation(siteList), [siteList]);

  const add = (auctionURL: string) =>
    run("add", "Collect", async () => {
      const result = await api.post<{ jobId: number; auctionId: string }>("/auctions", { url: auctionURL });
      setUrl("");
      return result;
    });

  const cleanup = async () => {
    if (
      !window.confirm(
        "Remove ended auctions and lots? Photos, analyses, and comparables for those records are deleted.",
      )
    ) {
      return;
    }
    setCleaning(true);
    setError(null);
    setNotice(null);
    try {
      const result = await api.post<CleanupResult>("/cleanup");
      auctions.reload();
      sites.reload();
      setNotice(cleanupMessage(result));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setCleaning(false);
    }
  };

  const refreshSites = () =>
    run("sites", "SITES list refresh", () => api.post<{ jobId: number }>("/sites/refresh"));

  return (
    <Page>
      <PageHeader
        title="Auctions"
        lede="Your collected auctions first, then SITES locations. Collection runs only when you ask."
      />
      <Stack>
        <BidrlTabs />
        <Notices message={notice} error={error} disabled={disabled} />
        <Card title="Add auction">
          <Stack>
            <Hint>
              Paste a BIDRL auction or print-catalog URL. Collection runs only when you ask;
              there is no crawl.
            </Hint>
            <Toolbar>
              <Field label="Auction URL">
                <Input
                  value={url}
                  onChange={(e) => setUrl(e.target.value)}
                  placeholder="https://www.bidrl.com/auction/…"
                  disabled={disabled || busy !== null}
                  aria-label="Auction URL"
                />
              </Field>
              <Button
                variant="primary"
                disabled={disabled || busy !== null || url.trim() === ""}
                onClick={() => void add(url)}
              >
                {busy === "add" ? "Queueing…" : "Add auction"}
              </Button>
            </Toolbar>
          </Stack>
        </Card>
        <Card
          title={`Collected auctions${collected.length > 0 ? ` (${collected.length})` : ""}`}
          actions={
            <Button size="sm" disabled={disabled || busy !== null || cleaning} onClick={() => void cleanup()}>
              {cleaning ? "Removing…" : "Remove ended"}
            </Button>
          }
        >
          <Hint>
            Ended auctions and lots stay until you remove them. The countdown is local from
            the last collect or bid refresh — nothing is deleted on a schedule.
          </Hint>
          {auctions.status === "loading" ? <Loading label="Loading auctions…" /> : null}
          {auctions.status === "error" && !disabled ? <Callout tone="danger">{auctions.error.message}</Callout> : null}
          {auctions.status === "ready" && collected.length > 0 ? (
            <LocationSections groups={collectedGroups} empty="No collected auctions yet.">
              {(group) => <CollectedTable auctions={group} />}
            </LocationSections>
          ) : auctions.status === "ready" ? (
            <EmptyState>No collected auctions yet. Add a URL or collect from SITES.</EmptyState>
          ) : null}
        </Card>
        <Card
          title="SITES by location"
          actions={
            <Button size="sm" disabled={disabled || busy !== null} onClick={() => void refreshSites()}>
              {busy === "sites" ? "Queueing…" : "Refresh list"}
            </Button>
          }
        >
          <Hint>
            Open auctions at BidRL’s SITES locations — the same family as{" "}
            <a href="https://www.bidrl.com/affiliate/turlock-19/" target="_blank" rel="noreferrer">Turlock</a>.
            Refresh is user-triggered; nothing is crawled on a schedule.
          </Hint>
          {sites.status === "ready" && siteList.length > 0 ? (
            <LocationSections groups={siteGroups} empty="No SITES list yet.">
              {(group) => (
                <SitesTable
                  auctions={group}
                  disabled={disabled}
                  busy={busy}
                  onCollect={(auctionURL) => void add(auctionURL)}
                />
              )}
            </LocationSections>
          ) : (
            <EmptyState>No SITES list yet. Refresh the list.</EmptyState>
          )}
        </Card>
      </Stack>
    </Page>
  );
}

/**
 * The one catalog. Its whole state — preset, text, bucket, category, ending, locations — lives in
 * the query string, so a filtered list can be linked to, and going into a lot and back
 * returns the list you left rather than a reset one.
 */
function LotsCatalog() {
  const [filter, setFilter] = useQueryState("filter");
  const [q, setQ] = useQueryState("q");
  const [bucket, setBucket] = useQueryState("bucket", "all");
  const [category, setCategory] = useQueryState("category", "all");
  const [ending, setEnding] = useQueryState("ending");
  const [affiliate, setAffiliate] = useQueryState("affiliate");
  const [draft, setDraft] = useState(q);
  const [view, setView] = useLotView();
  const snap = useLots(filter, q, bucket, category, ending, affiliate);
  const locations = useLocations();
  const disabled = snap.error instanceof PluginDisabledError;
  const selected = parseAffiliateParam(affiliate);
  const narrowed =
    Boolean(filter || q || ending || affiliate) || bucket !== "all" || category !== "all";

  const toggleLocation = (id: string) => {
    setAffiliate(
      affiliateParam(selected.includes(id) ? selected.filter((x) => x !== id) : [...selected, id]),
    );
  };

  // A query typed on another screen, or arrived at by link, has to show in the box.
  useEffect(() => setDraft(q), [q]);

  const clearAll = () => {
    setDraft("");
    setQ("");
    setFilter("");
    setBucket("all");
    setCategory("all");
    setEnding("");
    setAffiliate("");
  };

  return (
    <Page>
      <PageHeader
        title="Lots"
        lede="Every collected lot. Start from a preset, then narrow by text, bucket, category, or the locations you can actually drive to. Looking for something by purpose rather than by word? Ask on the Intent tab."
      />
      <Stack>
        <BidrlTabs />
        <Notices message={null} error={null} disabled={disabled} />
        <Card
          title="Catalog"
          actions={
            <>
              {narrowed ? (
                <Button size="sm" onClick={clearAll}>
                  Clear filters
                </Button>
              ) : null}
              <ViewToggle value={view} onChange={setView} />
            </>
          }
        >
          <Toolbar>
            <Field label="Show">
              <Select value={filter} onChange={(e) => setFilter(e.target.value)} aria-label="Preset">
                {LOT_PRESETS.map((preset) => (
                  <option key={preset.value} value={preset.value}>{preset.label}</option>
                ))}
              </Select>
            </Field>
            <Field label="Find">
              <Input
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") setQ(draft);
                }}
                placeholder="Title, identification, model, category…"
                disabled={disabled}
                aria-label="Lot search"
              />
            </Field>
            <Button disabled={disabled} onClick={() => setQ(draft)}>
              Find
            </Button>
            <Field label="Bucket">
              <Select value={bucket} onChange={(e) => setBucket(e.target.value)} aria-label="Bucket">
                <option value="all">All buckets</option>
                <option value="priced">Priced</option>
                <option value="worth_opening">Worth opening</option>
                <option value="research">Research</option>
                <option value="skipped">Skipped</option>
                <option value="discarded">Discarded</option>
                <option value="pending">Pending</option>
              </Select>
            </Field>
            <Field label="Category">
              <Select value={category} onChange={(e) => setCategory(e.target.value)} aria-label="Category">
                <option value="all">All categories</option>
                {LOT_CATEGORIES.map((c) => (
                  <option key={c} value={c}>{c}</option>
                ))}
              </Select>
            </Field>
            <Checkbox
              label="Ending soon"
              checked={ending === "soon"}
              onChange={(e) => setEnding(e.target.checked ? "soon" : "")}
              disabled={disabled}
            />
          </Toolbar>
          {locations.status === "ready" && locations.data.locations.length > 0 ? (
            <Field label="Locations">
              <div className="bidrl-loc-filter">
                {locations.data.locations.map((loc) => (
                  <Button
                    key={loc.id}
                    size="sm"
                    pressed={selected.includes(loc.id)}
                    disabled={disabled}
                    onClick={() => toggleLocation(loc.id)}
                  >
                    {locationLabel(loc)} · {loc.lotCount}
                  </Button>
                ))}
                {selected.length > 0 ? (
                  <Button size="sm" onClick={() => setAffiliate("")}>
                    All locations
                  </Button>
                ) : null}
              </div>
            </Field>
          ) : null}
          <Hint>
            {filterLabel(filter)}
            {snap.status === "ready" ? ` · ${snap.data.lots.length} shown` : ""}
          </Hint>
          {snap.status === "loading" ? <Loading label="Loading lots…" /> : null}
          {snap.status === "error" && !disabled ? <Callout tone="danger">{snap.error.message}</Callout> : null}
          {snap.status === "ready" ? (
            <LotBrowser
              lots={snap.data.lots}
              empty={
                narrowed
                  ? "No lot matches these filters. Clear them to see the whole catalog."
                  : "No lots collected yet. Collect an auction on the Auctions tab."
              }
              view={view}
            />
          ) : null}
        </Card>
      </Stack>
    </Page>
  );
}

function IntentSearch() {
  const [intentDraft, setIntentDraft] = useState("");
  const [intentBusy, setIntentBusy] = useState(false);
  const [intentError, setIntentError] = useState<string | null>(null);
  const [view, setView] = useLotView();
  const intent = useIntent();
  const disabled = intent.error instanceof PluginDisabledError;
  const search = intent.status === "ready" ? intent.data.search : null;
  const intentLots = intent.status === "ready" ? intent.data.lots : [];
  const intentRunning = search?.status === "queued" || search?.status === "running" || intentBusy;

  const ask = async () => {
    const query = intentDraft.trim();
    if (!query || intentRunning) return;
    setIntentBusy(true);
    setIntentError(null);
    try {
      await api.post("/intent", { query });
      intent.reload();
    } catch (err) {
      setIntentError(err instanceof Error ? err.message : String(err));
    } finally {
      setIntentBusy(false);
    }
  };

  return (
    <Page>
      <PageHeader
        title="Intent"
        lede="Ask for what you actually want — camping gear, not the word camp."
      />
      <Stack>
        <BidrlTabs />
        <Notices message={null} error={intentError} disabled={disabled} />
        <Card title="Intent">
          <Stack>
            <Hint>
              One chat call turns your intent into related gear (headlamp, lantern, tent —
              not only the word you typed). Those words are embedded and ranked against
              collected titles and descriptions. Photo identifications count too when a
              lot has already been scanned.
            </Hint>
            <Field label="What are you looking to do?">
              <Textarea
                value={intentDraft}
                onChange={(e) => setIntentDraft(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && !e.shiftKey) {
                    e.preventDefault();
                    void ask();
                  }
                }}
                placeholder="Things that would help me camp"
                disabled={disabled || intentRunning}
                rows={2}
                aria-label="Intent search"
              />
            </Field>
            <div className="bidrl-actions">
              <Button variant="primary" disabled={disabled || intentRunning || !intentDraft.trim()} onClick={() => void ask()}>
                {intentRunning ? "Matching…" : "Ask"}
              </Button>
              <Hint>Enter to ask, Shift+Enter for a new line.</Hint>
            </div>
            <div className="bidrl-examples">
              <span className="bidrl-examples__label">Try</span>
              {INTENT_EXAMPLES.map((example) => (
                <button
                  key={example}
                  type="button"
                  className="bidrl-chip"
                  disabled={disabled || intentRunning}
                  onClick={() => setIntentDraft(example)}
                >
                  {example}
                </button>
              ))}
            </div>
            {search?.status === "failed" && search.lastError ? (
              <Callout tone="danger">{search.lastError}</Callout>
            ) : null}
            {search && search.status !== "failed" ? (
              <Hint>
                {search.status === "ready"
                  ? `${search.hitCount} match${search.hitCount === 1 ? "" : "es"} of ${search.scanned} lots`
                  : `Looking through ${search.scanned || "collected"} lots…`}
                {search.skipped > 0 ? ` · ${search.skipped} from title or description` : ""}
                {search.query ? ` · “${search.query}”` : ""}
              </Hint>
            ) : null}
          </Stack>
        </Card>
        {search && (search.status === "ready" || intentRunning) ? (
          <Card title="Matches" actions={<ViewToggle value={view} onChange={setView} />}>
            {intent.status === "loading" || intentRunning ? <Loading label="Matching lots to your intent…" /> : null}
            {intent.status === "error" && !disabled ? <Callout tone="danger">{intent.error.message}</Callout> : null}
            {intent.status === "ready" && search.status === "ready" ? (
              <LotBrowser
                lots={intentLots}
                empty="Nothing in the collected lots serves that intent."
                view={view}
                groupSimilar={false}
              />
            ) : null}
          </Card>
        ) : (
          <Card title="Matches">
            <EmptyState>
              Describe what you want to do and BIDRL ranks every collected lot against it. Nothing is
              fetched from BidRL and no photograph is sent — this reads titles and descriptions you
              have already collected.
            </EmptyState>
          </Card>
        )}
      </Stack>
    </Page>
  );
}

function AuctionView() {
  const id = useRouteParams().id ?? "";
  const snap = useAuction(id);
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
        actions={
          <div className="bidrl-actions">
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
            <Button variant="danger" disabled={disabled || pending} onClick={() => void remove()}>
              {deleting ? "Deleting…" : "Delete"}
            </Button>
            {auction?.url ? <BidrlLink href={auction.url} /> : null}
          </div>
        }
      />
      <Stack>
        <BidrlTabs />
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
        {snap.status === "ready" ? (
          <Card title="Lots" actions={<ViewToggle value={view} onChange={setView} />}>
            <LotBrowser lots={snap.data.lots} empty="This auction has no lots yet." view={view} />
          </Card>
        ) : null}
      </Stack>
    </Page>
  );
}

function LotView() {
  const id = useRouteParams().id ?? "";
  const snap = useLot(id);
  const { busy, notice, error, run } = useAction();
  const disabled = snap.error instanceof PluginDisabledError;
  const lot = snap.status === "ready" ? snap.data : null;
  // Siblings come from the lot's own auction, so paging through a scan never leaves the
  // page to go back to a list and pick the next row.
  const siblings = useAuctionLots(lot?.auctionId ?? "");
  const neighbours = useMemo(() => lotNeighbours(siblings.lots, id), [siblings.lots, id]);
  return (
    <Page>
      <PageHeader
        title={lot?.title ?? "Lot"}
        lede={lot?.lotCode ? `Lot ${lot.lotCode}` : undefined}
        actions={
          <div className="bidrl-actions">
            <Button
              variant="primary"
              disabled={disabled || busy !== null || (lot != null && lot.basis !== "exact_text" && lot.basis !== "barcode")}
              title={
                lot != null && lot.basis !== "exact_text" && lot.basis !== "barcode"
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
            {lot?.url ? <BidrlLink href={lot.url} /> : null}
          </div>
        }
      />
      <Stack>
        <BidrlTabs />
        {lot ? (
          <div className="bidrl-crumbs">
            <Link to="/bidrl/lots">Lots</Link>
            <span aria-hidden="true">/</span>
            <Link to={`/bidrl/auction/${encodeURIComponent(lot.auctionId)}`}>
              {siblings.title || "Auction"}
            </Link>
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

function Tile({ enabled }: PluginSurfaceProps) {
  const snap = useFeed("deals", "");
  if (snap.status === "loading") return <Hint>Loading…</Hint>;
  if (snap.status === "error") {
    return <Hint>{snap.error instanceof PluginDisabledError ? "Disabled." : snap.error.message}</Hint>;
  }
  const best = snap.data.lots[0];
  if (!best) {
    return <Hint>{enabled ? "No priced lots yet." : "Disabled."}</Hint>;
  }
  return (
    <Stack>
      <Row>
        <Badge tone="ok">{best.title}</Badge>
        {best.dealScore != null ? (
          <span className="cc-hint">{Math.round(best.dealScore * 100)}% under comparable</span>
        ) : null}
      </Row>
      <Hint>
        {cents(best.currentBidCents)} bid
        {best.priceCents != null ? ` · ${cents(best.priceCents)} ${comparableHint(best)}` : ""}
        {best.endsAt ? (
          <>
            {" · "}
            <Countdown iso={best.endsAt} />
          </>
        ) : null}
      </Hint>
    </Stack>
  );
}

function Detail({ enabled }: PluginSurfaceProps) {
  const snap = useFeed("deals", "");
  if (snap.status !== "ready") return <Hint>Loading…</Hint>;
  if (snap.data.lots.length === 0) {
    return <Hint>{enabled ? "No priced lots yet." : "Disabled."}</Hint>;
  }
  return <LotBrowser lots={snap.data.lots.slice(0, 8)} empty="No priced lots yet." view="table" />;
}

const bidrl: PluginModule = {
  id: "bidrl",
  nav: [{ path: "/bidrl", label: "BIDRL" }],
  routes: [
    { path: "/bidrl", element: <Overview /> },
    { path: "/bidrl/auctions", element: <Auctions /> },
    { path: "/bidrl/lots", element: <LotsCatalog /> },
    { path: "/bidrl/intent", element: <IntentSearch /> },
    { path: "/bidrl/auction/:id", element: <AuctionView /> },
    { path: "/bidrl/lot/:id", element: <LotView /> },
  ],
  dashboard: {
    summary: "Scores lots from photographs. Collect a SITES auction, then scan.",
    live: ["bidrl.**"],
    tile: Tile,
    detail: Detail,
  },
};

export default bidrl;
