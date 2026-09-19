/** Auctions: what is collected, and what SITES is offering. */
import {
  useMemo,
  useState,
} from "react";
import {
  Button,
  Callout,
  Card,
  Countdown,
  Dash,
  EmptyState,
  Field,
  Hint,
  Input,
  Link,
  Loading,
  Page,
  PageHeader,
  PluginDisabledError,
  Stack,
  Table,
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
  type SitesSortColumn,
  type LocationGroup,
} from "../model";
import { api } from "../api";
import { useAuctions, useSites } from "../data";
import { useColumnSort, SortedHead } from "../sorting";
import { BidrlLink, BidrlTabs } from "../chrome";
import { useAction, Notices } from "../actions";

function CollectedTable({ auctions }: { auctions: Auction[] }) {
  const { sort, onSort } = useColumnSort<AuctionSortColumn>(AUCTION_SORT_DEFAULTS);
  const rows = useMemo(() => sortAuctions(auctions, sort), [auctions, sort]);
  return (
    <Table
      className="bidrl-auctions-table"
      head={
        <>
          <SortedHead column="title" sort={sort} onSort={onSort}>Auction</SortedHead>
          <th>Location</th>
          <SortedHead column="status" sort={sort} onSort={onSort}>Status</SortedHead>
          <SortedHead column="lots" sort={sort} onSort={onSort} numeric>Lots</SortedHead>
          <SortedHead column="ends" sort={sort} onSort={onSort}>Ends</SortedHead>
          <th><span className="cc-sr-only">Open</span></th>
        </>
      }
    >
      {rows.map((a) => (
        <tr key={a.id}>
          <td className="bidrl-auction-table__name">
            <Link to={`/bidrl/auction/${encodeURIComponent(a.id)}`}>{a.title || a.id}</Link>
            {a.url ? <Hint><BidrlLink href={a.url} /></Hint> : null}
          </td>
          <td className="bidrl-auction-table__location">{locationLabel(a)}</td>
          <td>
            <span className="bidrl-auction-status">
              <span className={`bidrl-auction-status__dot bidrl-auction-status__dot--${statusTone(a.status)}`} />
              {a.status || "Unknown"}
            </span>
          </td>
          <td className="cc-num">{a.lotCount}</td>
          <td>{a.endsAt ? <Countdown iso={a.endsAt} /> : <Dash />}</td>
          <td className="bidrl-auction-table__action">
            <Link to={`/bidrl/auction/${encodeURIComponent(a.id)}`}>Open</Link>
          </td>
        </tr>
      ))}
    </Table>
  );
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

function SiteSourceOverview({ groups }: { groups: LocationGroup<SitesAuction>[] }) {
  return (
    <div className="bidrl-sites-overview" aria-label="SITES source overview">
      {groups.map((group) => {
        const collected = group.items.filter((auction) => auction.collected).length;
        const lots = group.items.reduce((total, auction) => total + auction.itemCount, 0);
        return (
          <article className="bidrl-source-summary" key={group.key}>
            <span className="bidrl-eyebrow">SITES source</span>
            <strong>{group.label}</strong>
            <span>{group.items.length} auctions · {lots} lots</span>
            <small>{collected} collected</small>
          </article>
        );
      })}
    </div>
  );
}

function SitesIncomingTable({
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
      className="bidrl-sites-table"
      head={
        <>
          <th>Location</th>
          <SortedHead column="title" sort={sort} onSort={onSort}>Auction</SortedHead>
          <SortedHead column="lots" sort={sort} onSort={onSort} numeric>Lots</SortedHead>
          <SortedHead column="ends" sort={sort} onSort={onSort}>Ends</SortedHead>
          <th><span className="cc-sr-only">Collection</span></th>
        </>
      }
    >
      {rows.map((auction) => (
        <tr key={auction.id}>
          <td className="bidrl-sites-table__location">{locationLabel(auction)}</td>
          <td className="bidrl-sites-table__name">
            {auction.collected ? (
              <Link to={`/bidrl/auction/${encodeURIComponent(auction.id)}`}>
                {auction.title || auction.id}
              </Link>
            ) : (
              <strong>{auction.title || auction.id}</strong>
            )}
            {auction.url ? <Hint><BidrlLink href={auction.url} /></Hint> : null}
          </td>
          <td className="cc-num">{auction.itemCount}</td>
          <td>{auction.endsAt ? <Countdown iso={auction.endsAt} /> : <Dash />}</td>
          <td className="bidrl-sites-table__action">
            {auction.collected ? (
              <span className="bidrl-row-state bidrl-row-state--ok">Collected</span>
            ) : (
              <Button
                size="sm"
                disabled={disabled || busy !== null}
                onClick={() => onCollect(auction.url)}
              >
                Collect
              </Button>
            )}
          </td>
        </tr>
      ))}
    </Table>
  );
}

function SiteCards({
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
  return (
    <>
      <SiteSourceOverview groups={groups} />
      <div className="bidrl-sites-incoming">
        <div className="bidrl-surface__subhead">
          <div>
            <span className="bidrl-eyebrow">Incoming auctions</span>
            <strong>Available to collect</strong>
          </div>
          <Hint>{groups.reduce((count, group) => count + group.items.length, 0)} listed</Hint>
        </div>
        <SitesIncomingTable
          auctions={groups.flatMap((group) => group.items)}
          disabled={disabled}
          busy={busy}
          onCollect={onCollect}
        />
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
        eyebrow="BidRL"
        title={showingSites ? "Sites" : "Auctions"}
        lede={showingSites
          ? "Browse available SITES locations and collect the auctions worth scanning."
          : "Collected auctions and the next work waiting in the queue."}
      />
      <Stack>
        <BidrlTabs />
        <Notices message={notice} error={error} disabled={pluginDisabled} />
        {!showingSites ? (
          <>
            <Card title="Add auction" className="bidrl-surface bidrl-surface--quiet bidrl-intake-surface">
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
              className="bidrl-surface bidrl-auctions-surface"
              actions={
                <Button size="sm" disabled={disabled || busy !== null || cleaning} onClick={() => void cleanup()}>
                  {cleaning ? "Removing…" : "Remove ended"}
                </Button>
              }
            >
              <Hint>
                Ended auctions stay until you remove them. Collection and cleanup are always user-triggered.
              </Hint>
              {auctions.status === "loading" ? <Loading label="Loading auctions…" /> : null}
              {auctions.status === "error" && !disabled ? <Callout tone="danger">{auctions.error.message}</Callout> : null}
              {auctions.status === "ready" && collected.length > 0 ? (
                <CollectedTable auctions={collected} />
              ) : auctions.status === "ready" ? (
                <EmptyState>No collected auctions yet. Add a URL or collect from Sites.</EmptyState>
              ) : null}
            </Card>
          </>
        ) : null}
        {showingSites ? (
          <Card
            title="SITES locations"
            className="bidrl-surface bidrl-sites-surface"
            actions={
              <Button size="sm" disabled={disabled || busy !== null} onClick={() => void refreshSites()}>
                {busy === "sites" ? "Queueing…" : "Refresh list"}
              </Button>
            }
          >
            <Hint>Available auctions are grouped by location. Collect only the work you want to scan.</Hint>
            {sites.status === "loading" ? <Loading label="Loading Sites auctions…" /> : null}
            {sites.status === "error" && !sitesDisabled ? (
              <Callout tone="danger">{sites.error.message}</Callout>
            ) : null}
            {sites.status === "ready" && siteList.length > 0 ? (
              <SiteCards
                groups={siteGroups}
                disabled={pluginDisabled}
                busy={busy}
                onCollect={(auctionURL) => void add(auctionURL)}
              />
            ) : (
              <EmptyState>No Sites list yet. Refresh the list.</EmptyState>
            )}
          </Card>
        ) : null}
      </Stack>
    </Page>
  );
}
