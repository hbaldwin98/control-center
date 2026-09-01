/**
 * BIDRL — score auction lots from photographs, not titles.
 *
 * Imports `@cc/ui` and this directory only. Auction and lot ids come from the path so
 * this module never imports the shell router. One sidebar item; Feed / Auctions / Lots
 * are in-plugin tabs.
 */
import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import {
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
  Loading,
  Metric,
  Page,
  PageHeader,
  PluginAIHint,
  PluginDisabledError,
  RelativeTime,
  Row,
  Select,
  Stack,
  Table,
  Tabs,
  Textarea,
  Toolbar,
  pluginApi,
  useSnapshot,
} from "@cc/ui";
import type { PluginModule, PluginSurfaceProps, UseSnapshotResult } from "@cc/ui";
import {
  LOT_CATEGORIES,
  cents,
  cleanupMessage,
  comparableHint,
  eventBoundary,
  filterLabel,
  groupByLocation,
  groupSimilarLots,
  type Auction,
  type AuctionPage,
  type AuctionsPage,
  type CleanupResult,
  type FeedPage,
  type IntentPage,
  type LocationGroup,
  type Lot,
  type LotsPage,
  type SimilarGroup,
  type SitesAuction,
  type SitesPage,
} from "./model";
import "./index.css";

const api = pluginApi("bidrl");

function pathParts(): string[] {
  return window.location.pathname.split("/").filter(Boolean);
}

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

function useLots(q: string, bucket: string, category: string, ending: string): UseSnapshotResult<LotsPage> {
  const load = useCallback(async (signal: AbortSignal) => {
    const params = new URLSearchParams();
    if (q.trim()) params.set("q", q.trim());
    if (bucket && bucket !== "all") params.set("bucket", bucket);
    if (category && category !== "all") params.set("category", category);
    if (ending) params.set("ending", ending);
    const qs = params.toString();
    const data = await api.get<LotsPage>(`/lots${qs ? `?${qs}` : ""}`, signal);
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, [q, bucket, category, ending]);
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

function ViewToggle({ value, onChange }: { value: "grid" | "table"; onChange: (view: "grid" | "table") => void }) {
  return (
    <div className="bidrl-actions">
      <Button size="sm" pressed={value === "grid"} onClick={() => onChange("grid")}>
        Grid
      </Button>
      <Button size="sm" pressed={value === "table"} onClick={() => onChange("table")}>
        Table
      </Button>
    </div>
  );
}

function bucketTone(bucket: string): "neutral" | "ok" | "warn" | "danger" {
  if (bucket === "priced") return "ok";
  if (bucket === "worth_opening" || bucket === "research") return "warn";
  if (bucket === "discarded" || bucket === "rejected") return "danger";
  return "neutral";
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
  return (
    <Table head={<><th>Auction</th><th>Status</th><th>Lots</th><th>Ends</th></>}>
      {auctions.map((a) => (
        <tr key={a.id}>
          <td>
            <a href={`/bidrl/auction/${encodeURIComponent(a.id)}`}>{a.title || a.id}</a>
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
  return (
    <Table head={<><th>Auction</th><th>Lots</th><th>Ends</th><th></th></>}>
      {auctions.map((a) => (
        <tr key={a.id}>
          <td>
            {a.collected ? <a href={`/bidrl/auction/${encodeURIComponent(a.id)}`}>{a.title}</a> : a.title}
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

function BidrlTabs() {
  const path = window.location.pathname.replace(/\/+$/, "") || "/";
  const items = [
    { href: "/bidrl", label: "Feed" },
    { href: "/bidrl/auctions", label: "Auctions" },
    { href: "/bidrl/lots", label: "Lots" },
  ];
  return (
    <Tabs label="BIDRL sections">
      {items.map((item) => {
        const current =
          item.href === "/bidrl"
            ? path === "/bidrl"
            : item.href === "/bidrl/auctions"
              ? path === "/bidrl/auctions" || path.startsWith("/bidrl/auction/")
              : path === "/bidrl/lots" || path.startsWith("/bidrl/lot/");
        return (
          <a key={item.href} href={item.href} aria-current={current ? "page" : undefined}>
            {item.label}
          </a>
        );
      })}
    </Tabs>
  );
}

function LotThumb({ lot, className }: { lot: Lot; className?: string | undefined }) {
  if (!lot.thumbUrl) {
    return className ? <div className={`${className}-empty`}><Dash /></div> : <Dash />;
  }
  return <img className={className ?? "cc-lot-thumb"} src={lot.thumbUrl} alt="" width={className ? 220 : 48} height={className ? 220 : 48} />;
}

function LotThumbLink({ lot, className }: { lot: Lot; className?: string | undefined }) {
  const thumb = <LotThumb lot={lot} className={className} />;
  if (!lot.thumbUrl) return thumb;
  return (
    <a
      href={`/bidrl/lot/${encodeURIComponent(lot.id)}`}
      className={className ? `${className}-link` : undefined}
      aria-label={`Open ${lot.title || lot.id}`}
    >
      {thumb}
    </a>
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
      <span>{cents(lot.currentBidCents)}</span>
      {lot.endsAt ? <Countdown iso={lot.endsAt} /> : <Dash />}
      {lot.category ? <Badge>{lot.category}</Badge> : null}
      <Badge tone={bucketTone(lot.bucket)}>{lot.bucket.replace("_", " ")}</Badge>
    </>
  );
}

function LotTitle({ lot }: { lot: Lot }) {
  return (
    <>
      <a href={`/bidrl/lot/${encodeURIComponent(lot.id)}`}>{lot.title || lot.id}</a>
      <Hint>
        {lot.identification && lot.identification !== lot.title ? lot.identification : lot.lotCode}
        {lot.url ? (
          <>
            {" · "}
            <BidrlLink href={lot.url} />
          </>
        ) : null}
      </Hint>
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
          <td><LotTitle lot={lot} /></td>
          <td>{cents(lot.currentBidCents)}</td>
          <td>{lot.endsAt ? <Countdown iso={lot.endsAt} /> : <Dash />}</td>
          <td>{lot.category ? <Badge>{lot.category}</Badge> : <Dash />}</td>
          <td><LotComparable lot={lot} /></td>
          <td className="cc-num">{lot.dealScore != null ? `${Math.round(lot.dealScore * 100)}%` : <Dash />}</td>
          <td><Badge tone={bucketTone(lot.bucket)}>{lot.bucket.replace("_", " ")}</Badge></td>
          {showWhy ? <td>{lot.matchReason || <Dash />}</td> : null}
        </tr>
      ))}
    </>
  );
}

function LotCard({ lot }: { lot: Lot }) {
  return (
    <>
      <LotThumbLink lot={lot} className="bidrl-lot-card__img" />
      <div className="bidrl-lot-card__title"><LotTitle lot={lot} /></div>
      <div className="bidrl-lot-card__meta">
        <LotMeta lot={lot} />
      </div>
      {lot.priceCents != null ? (
        <Hint>
          {cents(lot.priceCents)}
          {lot.dealScore != null ? ` · ${Math.round(lot.dealScore * 100)}% gap` : ""}
          {comparableHint(lot) ? ` · ${comparableHint(lot)}` : ""}
        </Hint>
      ) : null}
      {lot.matchReason ? <p className="bidrl-intent-reason">{lot.matchReason}</p> : null}
    </>
  );
}

function SimilarList({ lots }: { lots: Lot[] }) {
  return (
    <div className="bidrl-similar">
      {lots.map((lot) => (
        <div key={lot.id} className="bidrl-similar__row">
          <a href={`/bidrl/lot/${encodeURIComponent(lot.id)}`}>{lot.lotCode || lot.title || lot.id}</a>
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
  const groups = useMemo(
    () => (groupSimilar ? groupSimilarLots(lots) : lots.map((lot) => ({ key: `id:${lot.id}`, label: lot.title, lots: [lot] }))),
    [lots, groupSimilar],
  );
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
      head={
        <>
          <th></th>
          <th>Lot</th>
          <th>Bid</th>
          <th>Ends</th>
          <th>Category</th>
          <th>Comparable</th>
          <th className="cc-num">Gap</th>
          <th>Bucket</th>
          {showWhy ? <th>Why</th> : null}
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
        <td>
          <LotTitle lot={head} />
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
        <td>{head.category ? <Badge>{head.category}</Badge> : <Dash />}</td>
        <td><LotComparable lot={head} /></td>
        <td className="cc-num">{head.dealScore != null ? `${Math.round(head.dealScore * 100)}%` : <Dash />}</td>
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
  message: string | null;
  error: string | null;
  disabled: boolean;
}) {
  return (
    <>
      {message ? <Callout tone="ok">{message}</Callout> : null}
      {error ? <Callout tone="danger">{error}</Callout> : null}
      {disabled ? (
        <Callout>
          BIDRL is disabled. Enable it on the <a href="/plugins/bidrl/settings">plugin screen</a>.
        </Callout>
      ) : null}
      <PluginAIHint pluginId="bidrl" />
    </>
  );
}

function Feed() {
  const [filter, setFilter] = useState("deals");
  const [draft, setDraft] = useState("");
  const [q, setQ] = useState("");
  const [view, setView] = useLotView();
  const snap = useFeed(filter, q);
  const disabled = snap.error instanceof PluginDisabledError;

  return (
    <Page>
      <PageHeader
        title="BIDRL"
        lede="Lots scored from photographs. A comparable appears only when a model or barcode is read and a search hit writes a dollar amount — eBay sold listings first."
      />
      <Stack>
        <BidrlTabs />
        <Notices message={null} error={null} disabled={disabled} />
        <Card
          title="Feed"
          actions={<ViewToggle value={view} onChange={setView} />}
        >
          <Toolbar>
            <Field label="Find">
              <Input
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") setQ(draft);
                }}
                placeholder="Title, identification, model…"
                disabled={disabled}
                aria-label="Find in feed"
              />
            </Field>
            <Button disabled={disabled} onClick={() => setQ(draft)}>
              Find
            </Button>
            <Field label="Show">
              <Select value={filter} onChange={(e) => setFilter(e.target.value)} aria-label="Feed filter">
                <option value="deals">Best deals</option>
                <option value="mislabeled">Likely mislabeled</option>
                <option value="model">Model number found</option>
                <option value="worth_opening">Worth opening</option>
                <option value="all">All scanned</option>
              </Select>
            </Field>
          </Toolbar>
          {snap.status === "loading" ? <Loading label="Loading feed…" /> : null}
          {snap.status === "error" && !disabled ? <Callout tone="danger">{snap.error.message}</Callout> : null}
          {snap.status === "ready" ? (
            <>
              <Hint>{filterLabel(filter)}</Hint>
              <LotBrowser lots={snap.data.lots} empty="Nothing in this filter yet. Collect an auction and run a scan." view={view} />
            </>
          ) : null}
        </Card>
      </Stack>
    </Page>
  );
}

function Auctions() {
  const auctions = useAuctions();
  const sites = useSites();
  const [url, setUrl] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [message, setMessage] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const disabled = auctions.error instanceof PluginDisabledError;
  const collected = auctions.status === "ready" ? auctions.data.auctions : [];
  const collectedGroups = useMemo(() => groupByLocation(collected), [collected]);
  const siteList = sites.status === "ready" ? sites.data.auctions : [];
  const siteGroups = useMemo(() => groupByLocation(siteList), [siteList]);

  const add = async (auctionURL: string) => {
    setBusy("add");
    setError(null);
    setMessage(null);
    try {
      const result = await api.post<{ jobId: number; auctionId: string }>("/auctions", { url: auctionURL });
      setMessage(`Collect queued as job ${result.jobId}.`);
      setUrl("");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  };

  const cleanup = async () => {
    if (
      !window.confirm(
        "Remove ended auctions and lots? Photos, analyses, and comparables for those records are deleted.",
      )
    ) {
      return;
    }
    setBusy("cleanup");
    setError(null);
    setMessage(null);
    try {
      const result = await api.post<CleanupResult>("/cleanup");
      auctions.reload();
      sites.reload();
      setMessage(cleanupMessage(result));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  };

  const refreshSites = async () => {
    setBusy("sites");
    setError(null);
    setMessage(null);
    try {
      const result = await api.post<{ jobId: number }>("/sites/refresh");
      setMessage(`SITES auction list queued as job ${result.jobId}.`);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  };

  return (
    <Page>
      <PageHeader
        title="Auctions"
        lede="Your collected auctions first, then SITES locations. Collection runs only when you ask."
      />
      <Stack>
        <BidrlTabs />
        <Notices message={message} error={error} disabled={disabled} />
        <Card
          title="Collected auctions"
          actions={
            <Button size="sm" disabled={disabled || busy !== null} onClick={() => void cleanup()}>
              {busy === "cleanup" ? "Removing…" : "Remove ended"}
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

function LotsCatalog() {
  const [draft, setDraft] = useState("");
  const [q, setQ] = useState("");
  const [bucket, setBucket] = useState("all");
  const [category, setCategory] = useState("all");
  const [endingSoon, setEndingSoon] = useState(false);
  const [intentDraft, setIntentDraft] = useState("");
  const [intentBusy, setIntentBusy] = useState(false);
  const [intentError, setIntentError] = useState<string | null>(null);
  const [view, setView] = useLotView();
  const snap = useLots(q, bucket, category, endingSoon ? "soon" : "");
  const intent = useIntent();
  const disabled = snap.error instanceof PluginDisabledError;
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
        title="Lots"
        lede="Every collected lot. Filter by text, or ask what you actually want — camping gear, not the word camp."
      />
      <Stack>
        <BidrlTabs />
        <Notices message={null} error={intentError} disabled={disabled} />
        <PluginAIHint pluginId="bidrl" />
        <Card title="Intent">
          <Stack>
            <Hint>
              Matches collected titles and descriptions. Photo identifications count too
              when a lot has already been scanned — you do not need to scan first. One
              cheap expansion call, not a pass over every photograph.
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
        ) : null}
        <Card title="Filter">
          <Toolbar>
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
              checked={endingSoon}
              onChange={(e) => setEndingSoon(e.target.checked)}
              disabled={disabled}
            />
          </Toolbar>
        </Card>
        <Card title="Catalog" actions={<ViewToggle value={view} onChange={setView} />}>
          {snap.status === "loading" ? <Loading label="Loading lots…" /> : null}
          {snap.status === "error" && !disabled ? <Callout tone="danger">{snap.error.message}</Callout> : null}
          {snap.status === "ready" ? (
            <LotBrowser lots={snap.data.lots} empty="No lots match these filters." view={view} />
          ) : null}
        </Card>
      </Stack>
    </Page>
  );
}

function AuctionView() {
  const id = pathParts()[2] ?? "";
  const snap = useAuction(id);
  const [view, setView] = useLotView();
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const run = async (action: "scan" | "refresh") => {
    setBusy(action);
    setError(null);
    try {
      await api.post(`/auctions/${encodeURIComponent(id)}/${action}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  };
  const remove = async () => {
    setBusy("delete");
    setError(null);
    try {
      await api.del(`/auctions/${encodeURIComponent(id)}`);
      window.location.href = "/bidrl/auctions";
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setBusy(null);
    }
  };
  const disabled = snap.error instanceof PluginDisabledError;
  const auction = snap.status === "ready" ? snap.data.auction : null;
  return (
    <Page>
      <PageHeader
        title={auction?.title ?? "Auction"}
        lede={auction ? undefined : "Auction"}
        actions={
          <div className="bidrl-actions">
            <Button variant="primary" disabled={disabled || busy !== null} onClick={() => void run("scan")}>
              {busy === "scan" ? "Queueing…" : "Scan"}
            </Button>
            <Button disabled={disabled || busy !== null} onClick={() => void run("refresh")}>
              {busy === "refresh" ? "Queueing…" : "Refresh bids"}
            </Button>
            <Button disabled={disabled || busy !== null} onClick={() => void remove()}>
              Delete
            </Button>
            {auction?.url ? <BidrlLink href={auction.url} /> : null}
          </div>
        }
      />
      <Stack>
        <BidrlTabs />
        {error ? <Callout tone="danger">{error}</Callout> : null}
        <PluginAIHint pluginId="bidrl" />
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
  const id = pathParts()[2] ?? "";
  const snap = useLot(id);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const act = async (action: "reprice" | "enrich") => {
    setBusy(action);
    setError(null);
    try {
      await api.post(`/lots/${encodeURIComponent(id)}/${action}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  };
  const disabled = snap.error instanceof PluginDisabledError;
  const lot = snap.status === "ready" ? snap.data : null;
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
              onClick={() => void act("reprice")}
            >
              {busy === "reprice" ? "Queueing…" : "Reprice"}
            </Button>
            <Button disabled={disabled || busy !== null} onClick={() => void act("enrich")}>
              {busy === "enrich" ? "Queueing…" : "Enrich"}
            </Button>
            {lot ? <a href={`/bidrl/auction/${encodeURIComponent(lot.auctionId)}`}>Auction</a> : null}
            {lot?.url ? <BidrlLink href={lot.url} /> : null}
          </div>
        }
      />
      <Stack>
        <BidrlTabs />
        {error ? <Callout tone="danger">{error}</Callout> : null}
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
                value={lot.dealScore != null ? `${Math.round(lot.dealScore * 100)}%` : <Dash />}
              />
              <Metric label="Ends" value={lot.endsAt ? <Countdown iso={lot.endsAt} /> : <Dash />} />
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
                      <a href={`/bidrl/lot/${encodeURIComponent(lot.reusedFromLotId)}`}>
                        lot {lot.reusedFromLotId}
                      </a>
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
    { path: "/bidrl", element: <Feed /> },
    { path: "/bidrl/auctions", element: <Auctions /> },
    { path: "/bidrl/lots", element: <LotsCatalog /> },
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
