/** Auctions: what is collected, and what SITES is offering. */
import {
  useMemo,
  useState,
} from "react";
import {
  Badge,
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
import { BidrlLink, LocationSections, BidrlTabs } from "../chrome";
import { useAction, Notices } from "../actions";

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
    <div className="bidrl-site-grid">
      {groups.map((group) => {
        const rows = sortSitesAuctions(group.items, {
          column: "title",
          dir: SITES_SORT_DEFAULTS.title,
        });
        const collected = rows.filter((auction) => auction.collected).length;
        return (
          <article className="bidrl-site-card" key={group.key}>
            <header className="bidrl-site-card__head">
              <div>
                <span className="bidrl-eyebrow">SITES location</span>
                <h3>{group.label}</h3>
              </div>
              <Badge tone={collected === rows.length ? "ok" : "neutral"}>
                {rows.length} auctions
              </Badge>
            </header>
            <div className="bidrl-site-card__rows">
              {rows.slice(0, 4).map((auction) => (
                <div className="bidrl-site-card__row" key={auction.id}>
                  <div>
                    {auction.collected ? (
                      <Link to={`/bidrl/auction/${encodeURIComponent(auction.id)}`}>
                        {auction.title}
                      </Link>
                    ) : (
                      <strong>{auction.title}</strong>
                    )}
                    <small>
                      {auction.itemCount} lots
                      {auction.endsAt ? <> · <Countdown iso={auction.endsAt} /></> : null}
                    </small>
                  </div>
                  {auction.collected ? (
                    <span className="bidrl-site-card__state">Collected</span>
                  ) : (
                    <Button
                      size="sm"
                      disabled={disabled || busy !== null}
                      onClick={() => onCollect(auction.url)}
                    >
                      Collect
                    </Button>
                  )}
                </div>
              ))}
            </div>
            <footer className="bidrl-site-card__foot">
              <span>{collected} of {rows.length} collected</span>
              {rows.length > 4 ? <span>+{rows.length - 4} more</span> : null}
            </footer>
          </article>
        );
      })}
    </div>
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
  const collectedGroups = useMemo(() => groupByLocation(collected), [collected]);
  const siteList = sites.status === "ready" ? sites.data.auctions : [];
  const siteGroups = useMemo(() => groupByLocation(siteList), [siteList]);
  const showingSites = view === "sites";

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
        <Notices message={notice} error={error} disabled={disabled} />
        {!showingSites ? (
          <>
            <Card title="Add auction" className="bidrl-surface bidrl-surface--quiet">
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
              className="bidrl-surface"
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
                <LocationSections groups={collectedGroups} empty="No collected auctions yet.">
                  {(group) => <CollectedTable auctions={group} />}
                </LocationSections>
              ) : auctions.status === "ready" ? (
                <EmptyState>No collected auctions yet. Add a URL or collect from Sites.</EmptyState>
              ) : null}
            </Card>
          </>
        ) : null}
        {showingSites ? (
          <Card
            title="SITES locations"
            className="bidrl-surface"
            actions={
              <Button size="sm" disabled={disabled || busy !== null} onClick={() => void refreshSites()}>
                {busy === "sites" ? "Queueing…" : "Refresh list"}
              </Button>
            }
          >
            <Hint>Available auctions are grouped by location. Collect only the work you want to scan.</Hint>
            {sites.status === "ready" && siteList.length > 0 ? (
              <SiteCards
                groups={siteGroups}
                disabled={disabled}
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
