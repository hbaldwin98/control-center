/** Auctions: what is collected, and what SITES is offering. */
import {
  useMemo,
  useState,
} from "react";
import {
  Button,
  Callout,
  Countdown,
  Dash,
  EmptyState,
  Field,
  Input,
  Link,
  Loading,
  Page,
  PageHeader,
  Panel,
  PluginDisabledError,
  RelativeTime,
  Stack,
  Toolbar,
  useQueryState,
} from "@cc/ui";
import {
  AUCTION_SORT_DEFAULTS,
  SITES_SORT_DEFAULTS,
  cleanupMessage,
  groupByLocation,
  locationLabel,
  sortAuctions,
  sortSitesAuctions,
  type Auction,
  type AuctionSortColumn,
  type CleanupResult,
  type SitesAuction,
  type LocationGroup,
} from "../model";
import { api } from "../api";
import { useAuctions, useSites } from "../data";
import { useColumnSort, SortedHead } from "../sorting";
import { BidrlTabs } from "../chrome";
import { useAction, Notices } from "../actions";

function toneClass(tone: "ok" | "warn" | "danger" | "neutral"): string {
  if (tone === "ok") return " success";
  if (tone === "neutral") return "";
  return ` ${tone}`;
}

function statusTone(status: string): "ok" | "warn" | "danger" | "neutral" {
  const normalized = status.toLowerCase();
  if (normalized.includes("error") || normalized.includes("fail")) return "danger";
  if (normalized.includes("pending") || normalized.includes("scan")) return "warn";
  if (normalized.includes("ready") || normalized.includes("active") || normalized.includes("open")) {
    return "ok";
  }
  return "neutral";
}

function CollectedTable({ auctions }: { auctions: Auction[] }) {
  const { sort, onSort } = useColumnSort<AuctionSortColumn>(AUCTION_SORT_DEFAULTS);
  const rows = useMemo(() => sortAuctions(auctions, sort), [auctions, sort]);
  return (
    <div className="data-table-wrap">
      <table className="data-table bidrl-auctions-table">
        <thead>
          <tr>
            <SortedHead column="title" sort={sort} onSort={onSort}>
              Auction site
            </SortedHead>
            <th>City</th>
            <SortedHead column="lots" sort={sort} onSort={onSort}>
              Lots
            </SortedHead>
            <SortedHead column="ends" sort={sort} onSort={onSort}>
              Ends
            </SortedHead>
            <SortedHead column="status" sort={sort} onSort={onSort}>
              Health
            </SortedHead>
            <th className="cc-table__actions">
              <span className="cc-sr-only">Open</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {rows.map((a) => (
            <tr key={a.id}>
              <td className="bidrl-auction-table__name">
                <Link to={`/bidrl/auction/${encodeURIComponent(a.id)}`}>
                  {a.title || a.id}
                </Link>
                <small>
                  {a.lastError ? (
                    a.lastError
                  ) : a.collectedAt ? (
                    <>
                      Synced <RelativeTime at={a.collectedAt} />
                    </>
                  ) : (
                    "Not yet collected"
                  )}
                </small>
              </td>
              <td>{locationLabel(a) || <Dash />}</td>
              <td>{a.lotCount}</td>
              <td>{a.endsAt ? <Countdown iso={a.endsAt} /> : <Dash />}</td>
              <td>
                <span className={`pill${toneClass(statusTone(a.status))}`}>
                  {a.status || "unknown"}
                </span>
              </td>
              <td>
                <Link
                  className="table-action"
                  to={`/bidrl/auction/${encodeURIComponent(a.id)}`}
                >
                  View
                </Link>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function SitesView({
  groups,
  disabled,
  busy,
  onCollect,
}: {
  groups: LocationGroup<SitesAuction>[];
  disabled: boolean;
  busy: string | null;
  onCollect: (url: string) => void;
}) {
  const [filter, setFilter] = useState("all");
  const [query, setQuery] = useState("");
  const totalAuctions = groups.reduce((count, group) => count + group.items.length, 0);
  const totalLots = groups.reduce(
    (count, group) => count + group.items.reduce((sum, auction) => sum + auction.itemCount, 0),
    0,
  );
  const totalCollected = groups.reduce(
    (count, group) => count + group.items.filter((auction) => auction.collected).length,
    0,
  );
  const needle = query.trim().toLowerCase();
  const visible = groups
    .filter((group) => filter === "all" || group.key === filter)
    .flatMap((group) => group.items)
    .filter((auction) => !needle || (auction.title || "").toLowerCase().includes(needle));
  const rows = sortSitesAuctions(visible, {
    column: "title",
    dir: SITES_SORT_DEFAULTS.title,
  });

  return (
    <>
      <div className="source-grid">
        <button
          type="button"
          className={`source-card${filter === "all" ? " active" : ""}`}
          onClick={() => setFilter("all")}
        >
          <div className="source-card-head">
            <strong>All sites</strong>
            <span className="pill success">{groups.length} online</span>
          </div>
          <p>Every connected city and auction site in this workspace.</p>
          <div className="source-meta">
            <span>{totalAuctions} auctions · {totalLots} lots</span>
            <span>{totalCollected} collected</span>
          </div>
        </button>
        {groups.map((group) => {
          const lots = group.items.reduce((sum, auction) => sum + auction.itemCount, 0);
          const collected = group.items.filter((auction) => auction.collected).length;
          return (
            <button
              key={group.key}
              type="button"
              className={`source-card${filter === group.key ? " active" : ""}`}
              onClick={() => setFilter(group.key)}
            >
              <div className="source-card-head">
                <strong>{group.label}</strong>
                <span className="pill success">{group.items.length} auctions</span>
              </div>
              <p>{group.label} auction site · {lots} lots tracked · {collected} collected.</p>
              <div className="source-meta">
                <span>{lots} lots</span>
                <span>{collected} collected</span>
              </div>
            </button>
          );
        })}
      </div>
      <div className="source-filters">
        <div
          className="source-filter-list"
          role="group"
          aria-label="Filter auctions by city or auction site"
        >
          <button
            type="button"
            className={`source-filter${filter === "all" ? " active" : ""}`}
            onClick={() => setFilter("all")}
          >
            All sites · {totalAuctions}
          </button>
          {groups.map((group) => (
            <button
              key={group.key}
              type="button"
              className={`source-filter${filter === group.key ? " active" : ""}`}
              onClick={() => setFilter(group.key)}
            >
              {group.label} · {group.items.length}
            </button>
          ))}
        </div>
        <Input
          className="bidrl-source-search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search incoming auctions…"
          aria-label="Search incoming auctions"
        />
      </div>
      <div className="data-table-wrap">
        <table className="data-table bidrl-sites-table">
          <thead>
            <tr>
              <th>Auction</th>
              <th>Auction site</th>
              <th>Lots</th>
              <th>Ends</th>
              <th>Intake</th>
              <th className="cc-table__actions">
                <span className="cc-sr-only">Collect</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {rows.map((auction) => (
              <tr key={auction.id}>
                <td className="bidrl-sites-table__name">
                  <strong>{auction.title || auction.id}</strong>
                  <small>
                    {auction.endsAt ? (
                      <>
                        Ends <Countdown iso={auction.endsAt} /> · {auction.itemCount} lots total
                      </>
                    ) : (
                      <>{auction.itemCount} lots total</>
                    )}
                  </small>
                </td>
                <td>
                  <span className="source-name">
                    <i className={`dot${auction.collected ? "" : " ready"}`} />
                    {locationLabel(auction)}
                  </span>
                </td>
                <td>{auction.itemCount}</td>
                <td>{auction.endsAt ? <Countdown iso={auction.endsAt} /> : <Dash />}</td>
                <td>
                  <span className={`pill${auction.collected ? " success" : " warn"}`}>
                    {auction.collected ? "collected" : "ready"}
                  </span>
                </td>
                <td>
                  {auction.collected ? (
                    <Link
                      className="table-action"
                      to={`/bidrl/auction/${encodeURIComponent(auction.id)}`}
                    >
                      Open
                    </Link>
                  ) : (
                    <button
                      type="button"
                      className="table-action primary"
                      disabled={disabled || busy !== null}
                      onClick={() => onCollect(auction.url)}
                    >
                      Collect
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}

export function Auctions() {
  const auctions = useAuctions();
  const sites = useSites();
  const [view] = useQueryState("view", "collected");
  const [url, setUrl] = useState("");
  const { busy, notice, error, run, setNotice, setError } = useAction();
  const [cleaning, setCleaning] = useState(false);
  const disabled = auctions.error instanceof PluginDisabledError;
  const collected = auctions.status === "ready" ? auctions.data.auctions : [];
  const siteList = sites.status === "ready" ? sites.data.auctions : [];
  const siteGroups = useMemo(() => groupByLocation(siteList), [siteList]);
  const showingSites = view === "sites";
  const sitesDisabled = sites.error instanceof PluginDisabledError;
  const pluginDisabled = disabled || sitesDisabled;

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
        eyebrow="Plugin / BidRL"
        title="Auction workspace"
        lede="See what is closing, what is underpriced, and what still needs a first pass."
      />
      <Stack>
        <BidrlTabs />
        <Notices message={notice} error={error} disabled={pluginDisabled} />
        {!showingSites ? (
          <>
            <Panel
              title="Add auction"
              subhead="Paste a BIDRL auction or print-catalog URL. Collection runs only when you ask; there is no crawl."
            >
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
            </Panel>
            <Panel
              title={`Collected auctions${collected.length > 0 ? ` (${collected.length})` : ""}`}
              subhead="Ended auctions stay until you remove them. Collection and cleanup are always user-triggered."
              actions={
                <Button size="sm" disabled={disabled || busy !== null || cleaning} onClick={() => void cleanup()}>
                  {cleaning ? "Removing…" : "Remove ended"}
                </Button>
              }
            >
              {auctions.status === "loading" ? <Loading label="Loading auctions…" /> : null}
              {auctions.status === "error" && !disabled ? <Callout tone="danger">{auctions.error.message}</Callout> : null}
              {auctions.status === "ready" && collected.length > 0 ? (
                <CollectedTable auctions={collected} />
              ) : auctions.status === "ready" ? (
                <EmptyState>No collected auctions yet. Add a URL or collect from Sites.</EmptyState>
              ) : null}
            </Panel>
          </>
        ) : null}
        {showingSites ? (
          <Panel
            title="Auction sites"
            subhead="Auction-site schedules and collection health across BidRL. Collect only the work you want to scan."
            actions={
              <Button size="sm" disabled={disabled || busy !== null} onClick={() => void refreshSites()}>
                {busy === "sites" ? "Queueing…" : "Refresh"}
              </Button>
            }
          >
            {sites.status === "loading" ? <Loading label="Loading Sites auctions…" /> : null}
            {sites.status === "error" && !sitesDisabled ? (
              <Callout tone="danger">{sites.error.message}</Callout>
            ) : null}
            {sites.status === "ready" && siteList.length > 0 ? (
              <SitesView
                groups={siteGroups}
                disabled={pluginDisabled}
                busy={busy}
                onCollect={(auctionURL) => void add(auctionURL)}
              />
            ) : (
              <EmptyState>No Sites list yet. Refresh the list.</EmptyState>
            )}
          </Panel>
        ) : null}
      </Stack>
    </Page>
  );
}
