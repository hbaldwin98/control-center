/** Auctions: what is collected, and what SITES is offering. */
import {
  useMemo,
  useState,
} from "react";
import {
  ActionsHeader,
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
  type SitesSortColumn,
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

export function Auctions() {
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
