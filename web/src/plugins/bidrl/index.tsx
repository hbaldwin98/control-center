/**
 * BIDRL — score auction lots from photographs, not titles.
 *
 * Imports `@cc/ui` and this directory only. Auction and lot ids come from the path so
 * this module never imports the shell router.
 */
import { useCallback, useState } from "react";
import {
  Badge,
  Button,
  Callout,
  Card,
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
  Row,
  Select,
  Stack,
  Table,
  pluginApi,
  useSnapshot,
} from "@cc/ui";
import type { PluginModule, PluginSurfaceProps, UseSnapshotResult } from "@cc/ui";
import {
  cents,
  eventBoundary,
  filterLabel,
  type AuctionPage,
  type AuctionsPage,
  type FeedPage,
  type Lot,
  type SearchPage,
  type SitesPage,
} from "./model";

const api = pluginApi("bidrl");

function pathParts(): string[] {
  return window.location.pathname.split("/").filter(Boolean);
}

function useFeed(filter: string): UseSnapshotResult<FeedPage> {
  const load = useCallback(async (signal: AbortSignal) => {
    const q = filter ? `?filter=${encodeURIComponent(filter)}` : "";
    const data = await api.get<FeedPage>(`/feed${q}`, signal);
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, [filter]);
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

function useSearch(): UseSnapshotResult<SearchPage> {
  const load = useCallback(async (signal: AbortSignal) => {
    const data = await api.get<SearchPage>("/search", signal);
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, []);
  return useSnapshot(load, { events: "bidrl.**" });
}

function bucketTone(bucket: string): "neutral" | "ok" | "warn" | "danger" {
  if (bucket === "priced") return "ok";
  if (bucket === "worth_opening" || bucket === "research") return "warn";
  if (bucket === "discarded" || bucket === "rejected") return "danger";
  return "neutral";
}

function LotTable({ lots }: { lots: Lot[] }) {
  if (lots.length === 0) {
    return <EmptyState>Nothing in this filter yet. Add an auction and run a scan.</EmptyState>;
  }
  return (
    <Table
      head={
        <>
          <th>Lot</th>
          <th>Bid</th>
          <th>Cited price</th>
          <th className="cc-num">Gap</th>
          <th>Bucket</th>
        </>
      }
    >
      {lots.map((lot) => (
        <tr key={lot.id}>
          <td>
            <a href={`/bidrl/lot/${encodeURIComponent(lot.id)}`}>{lot.title || lot.id}</a>
            <Hint>
              {lot.identification && lot.identification !== lot.title ? lot.identification : lot.lotCode}
            </Hint>
          </td>
          <td>{cents(lot.currentBidCents)}</td>
          <td>{lot.priceCents != null ? cents(lot.priceCents) : <Dash />}</td>
          <td className="cc-num">{lot.dealScore != null ? `${Math.round(lot.dealScore * 100)}%` : <Dash />}</td>
          <td><Badge tone={bucketTone(lot.bucket)}>{lot.bucket.replace("_", " ")}</Badge></td>
        </tr>
      ))}
    </Table>
  );
}

function Feed() {
  const [filter, setFilter] = useState("deals");
  const snap = useFeed(filter);
  const auctions = useAuctions();
  const sites = useSites();
  const searchSnap = useSearch();
  const [url, setUrl] = useState("");
  const [query, setQuery] = useState("");
  const [scope, setScope] = useState("prefer");
  const [busy, setBusy] = useState<string | null>(null);
  const [message, setMessage] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const disabled = snap.error instanceof PluginDisabledError;

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

  const search = async () => {
    setBusy("search");
    setError(null);
    setMessage(null);
    try {
      const result = await api.post<{ jobId: number }>("/search", { query, scope });
      setMessage(`Search queued as job ${result.jobId}. SITES locations rank first.`);
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
        title="BIDRL"
        lede="Search SITES auctions first, then score lots from photographs. A number appears only when a model or barcode is cited."
      />
      <Stack>
        {message ? <Callout tone="ok">{message}</Callout> : null}
        {error ? <Callout tone="danger">{error}</Callout> : null}
        {disabled ? (
          <Callout>
            BIDRL is disabled. Enable it on the <a href="/plugins/bidrl">plugin screen</a>.
          </Callout>
        ) : null}
        <PluginAIHint pluginId="bidrl" />
        <Card title="Search">
          <Stack>
            <Hint>
              Matches BidRL titles and any lots you have already scanned — so a chair titled
              "office mesh" still rises for "herman miller" if the photos said so. SITES
              locations (Turlock and the other dealers on that menu) rank first.
            </Hint>
            <Field label="Find">
              <Input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder="Keurig K-Supreme, DeWalt 20V, Herman Miller…"
                disabled={disabled || busy !== null}
                aria-label="Search query"
              />
            </Field>
            <Field label="Where">
              <Select value={scope} onChange={(e) => setScope(e.target.value)} aria-label="Search scope">
                <option value="prefer">SITES first, then everywhere</option>
                <option value="only">SITES locations only</option>
                <option value="all">All BidRL, no location boost</option>
              </Select>
            </Field>
            <Button
              variant="primary"
              disabled={disabled || busy !== null || query.trim() === ""}
              onClick={() => void search()}
            >
              {busy === "search" ? "Queueing…" : "Search"}
            </Button>
            {searchSnap.status === "ready" && searchSnap.data.search ? (
              <>
                <Hint>
                  {searchSnap.data.search.query} · {searchSnap.data.search.status}
                  {searchSnap.data.search.lastError ? ` · ${searchSnap.data.search.lastError}` : ""}
                </Hint>
                {searchSnap.data.hits.length === 0 ? (
                  <EmptyState>No matching lots yet. Search only runs when you ask.</EmptyState>
                ) : (
                  <Table
                    head={
                      <>
                        <th>Lot</th>
                        <th>Location</th>
                        <th>Bid</th>
                        <th>Why</th>
                        <th></th>
                      </>
                    }
                  >
                    {searchSnap.data.hits.map((hit) => (
                      <tr key={hit.url}>
                        <td>
                          {hit.collected ? (
                            <a href={`/bidrl/lot/${encodeURIComponent(hit.lotId)}`}>{hit.title}</a>
                          ) : (
                            <a href={hit.url} target="_blank" rel="noreferrer">{hit.title}</a>
                          )}
                          <Hint>{hit.auctionTitle || hit.auctionId}</Hint>
                        </td>
                        <td>
                          {hit.preferred ? <Badge tone="ok">{hit.affiliateName || "SITES"}</Badge> : (
                            <Badge>{hit.affiliateName || "other"}</Badge>
                          )}
                        </td>
                        <td>{cents(hit.currentBidCents)}</td>
                        <td><Hint>{hit.matchReason || hit.source}</Hint></td>
                        <td>
                          {hit.collected ? (
                            <a href={`/bidrl/auction/${encodeURIComponent(hit.auctionId)}`}>Collected</a>
                          ) : (
                            <Button
                              disabled={disabled || busy !== null || hit.auctionId === ""}
                              onClick={() => void add(`https://www.bidrl.com/auction/${hit.auctionId}/bidgallery`)}
                            >
                              Collect auction
                            </Button>
                          )}
                        </td>
                      </tr>
                    ))}
                  </Table>
                )}
              </>
            ) : null}
          </Stack>
        </Card>
        <Card
          title="SITES auctions"
          actions={
            <Button disabled={disabled || busy !== null} onClick={() => void refreshSites()}>
              {busy === "sites" ? "Queueing…" : "Refresh list"}
            </Button>
          }
        >
          <Hint>
            Open auctions at BidRL’s SITES locations — the same family as{" "}
            <a href="https://www.bidrl.com/affiliate/turlock-19/" target="_blank" rel="noreferrer">Turlock</a>.
            Refresh is user-triggered; nothing is crawled on a schedule.
          </Hint>
          {sites.status === "ready" && sites.data.auctions.length > 0 ? (
            <Table head={<><th>Auction</th><th>Location</th><th>Lots</th><th></th></>}>
              {sites.data.auctions.map((a) => (
                <tr key={a.id}>
                  <td>{a.collected ? <a href={`/bidrl/auction/${encodeURIComponent(a.id)}`}>{a.title}</a> : a.title}</td>
                  <td><Badge tone="ok">{a.affiliateName || a.city}</Badge></td>
                  <td className="cc-num">{a.itemCount}</td>
                  <td>
                    {a.collected ? "Collected" : (
                      <Button disabled={disabled || busy !== null} onClick={() => void add(a.url)}>
                        Collect
                      </Button>
                    )}
                  </td>
                </tr>
              ))}
            </Table>
          ) : (
            <EmptyState>No SITES list yet. Search, or refresh the list.</EmptyState>
          )}
        </Card>
        <Card title="Add auction">
          <Stack>
            <Hint>
              Paste a BIDRL auction or print-catalog URL. Collection runs only when you ask;
              there is no crawl.
            </Hint>
            <Field label="Auction URL">
              <Input
                value={url}
                onChange={(e) => setUrl(e.target.value)}
                placeholder="https://www.bidrl.com/auction/…"
                disabled={disabled || busy !== null}
                aria-label="Auction URL"
              />
            </Field>
            <Button variant="primary" disabled={disabled || busy !== null || url.trim() === ""} onClick={() => void add(url)}>
              {busy === "add" ? "Queueing…" : "Add auction"}
            </Button>
          </Stack>
        </Card>
        {auctions.status === "ready" && auctions.data.auctions.length > 0 ? (
          <Card title="Collected auctions">
            <Table head={<><th>Auction</th><th>Status</th><th>Lots</th></>}>
              {auctions.data.auctions.map((a) => (
                <tr key={a.id}>
                  <td><a href={`/bidrl/auction/${encodeURIComponent(a.id)}`}>{a.title || a.id}</a></td>
                  <td><Badge>{a.status}</Badge></td>
                  <td className="cc-num">{a.lotCount}</td>
                </tr>
              ))}
            </Table>
          </Card>
        ) : null}
        <Card
          title="Feed"
          actions={
            <Select value={filter} onChange={(e) => setFilter(e.target.value)} aria-label="Feed filter">
              <option value="deals">Best deals</option>
              <option value="mislabeled">Likely mislabeled</option>
              <option value="model">Model number found</option>
              <option value="worth_opening">Worth opening</option>
              <option value="all">All scanned</option>
            </Select>
          }
        >
          {snap.status === "loading" ? <Loading label="Loading feed…" /> : null}
          {snap.status === "error" && !disabled ? <Callout tone="danger">{snap.error.message}</Callout> : null}
          {snap.status === "ready" ? (
            <>
              <Hint>{filterLabel(filter)}</Hint>
              <LotTable lots={snap.data.lots} />
            </>
          ) : null}
        </Card>
      </Stack>
    </Page>
  );
}

function AuctionView() {
  const id = pathParts()[2] ?? "";
  const snap = useAuction(id);
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
      window.location.href = "/bidrl";
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setBusy(null);
    }
  };
  const disabled = snap.error instanceof PluginDisabledError;
  return (
    <Page>
      <PageHeader
        title={snap.status === "ready" ? snap.data.auction.title : "Auction"}
        lede={snap.status === "ready" ? snap.data.auction.url : undefined}
        actions={
          <Row>
            <Button variant="primary" disabled={disabled || busy !== null} onClick={() => void run("scan")}>
              {busy === "scan" ? "Queueing…" : "Scan"}
            </Button>
            <Button disabled={disabled || busy !== null} onClick={() => void run("refresh")}>
              {busy === "refresh" ? "Queueing…" : "Refresh bids"}
            </Button>
            <Button disabled={disabled || busy !== null} onClick={() => void remove()}>
              Delete
            </Button>
            <a href="/bidrl">Feed</a>
          </Row>
        }
      />
      <Stack>
        {error ? <Callout tone="danger">{error}</Callout> : null}
        <PluginAIHint pluginId="bidrl" />
        {snap.status === "loading" ? <Loading label="Loading auction…" /> : null}
        {snap.status === "error" && !disabled ? <Callout tone="danger">{snap.error.message}</Callout> : null}
        {snap.status === "ready" ? <LotTable lots={snap.data.lots} /> : null}
      </Stack>
    </Page>
  );
}

function LotView() {
  const id = pathParts()[2] ?? "";
  const snap = useLot(id);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const reprice = async () => {
    setBusy(true);
    setError(null);
    try {
      await api.post(`/lots/${encodeURIComponent(id)}/reprice`);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };
  const disabled = snap.error instanceof PluginDisabledError;
  const lot = snap.status === "ready" ? snap.data : null;
  return (
    <Page>
      <PageHeader
        title={lot?.title ?? "Lot"}
        lede={lot?.url}
        actions={
          <Row>
            <Button
              variant="primary"
              disabled={disabled || busy || (lot != null && lot.basis !== "exact_text" && lot.basis !== "barcode")}
              onClick={() => void reprice()}
            >
              {busy ? "Queueing…" : "Reprice"}
            </Button>
            {lot ? <a href={`/bidrl/auction/${encodeURIComponent(lot.auctionId)}`}>Auction</a> : null}
            <a href="/bidrl">Feed</a>
          </Row>
        }
      />
      <Stack>
        {error ? <Callout tone="danger">{error}</Callout> : null}
        {snap.status === "loading" ? <Loading label="Loading lot…" /> : null}
        {snap.status === "error" && !disabled ? <Callout tone="danger">{snap.error.message}</Callout> : null}
        {lot ? (
          <>
            <Grid density="metric">
              <Metric label="Bid" value={cents(lot.currentBidCents)} />
              <Metric
                label="Cited price"
                value={lot.priceCents != null ? cents(lot.priceCents) : "Unpriced"}
                hint={lot.priceKind || lot.basis}
              />
              <Metric
                label="Gap"
                value={lot.dealScore != null ? `${Math.round(lot.dealScore * 100)}%` : <Dash />}
              />
              <Metric label="Title agreement" value={`${Math.round(lot.titleAgreement * 100)}%`} />
            </Grid>
            {lot.photoUrls && lot.photoUrls.length > 0 ? (
              <Card title="Photos">
                <Row>
                  {lot.photoUrls.map((src) => (
                    <img key={src} src={src} alt="" width={160} height={160} />
                  ))}
                </Row>
              </Card>
            ) : null}
            <Card title="Identification">
              <Stack>
                <p>{lot.identification || lot.title}</p>
                <Hint>Basis: {lot.basis || "none"} {lot.modelOrSku ? `· ${lot.modelOrSku}` : ""}</Hint>
              </Stack>
            </Card>
            {lot.priceCents != null ? (
              <Card title="Evidence">
                <Stack>
                  <p>{lot.citedText || "Cited listing"}</p>
                  {lot.sourceUrl ? (
                    <a href={lot.sourceUrl} target="_blank" rel="noreferrer">{lot.sourceTitle || lot.sourceUrl}</a>
                  ) : null}
                  {lot.retrievedAt ? <Hint>Retrieved {lot.retrievedAt}</Hint> : null}
                </Stack>
              </Card>
            ) : (
              <Callout>
                No numeric valuation. Prices are stored only for exact text or barcode identification
                with a cited source that names the model, a price, and a condition.
              </Callout>
            )}
          </>
        ) : null}
      </Stack>
    </Page>
  );
}

function Tile({ enabled }: PluginSurfaceProps) {
  const snap = useFeed("deals");
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
          <span className="cc-hint">{Math.round(best.dealScore * 100)}% under cited price</span>
        ) : null}
      </Row>
      <Hint>
        {cents(best.currentBidCents)} bid
        {best.priceCents != null ? ` · ${cents(best.priceCents)} cited` : ""}
      </Hint>
    </Stack>
  );
}

function Detail({ enabled }: PluginSurfaceProps) {
  const snap = useFeed("deals");
  if (snap.status !== "ready") return <Hint>Loading…</Hint>;
  if (snap.data.lots.length === 0) {
    return <Hint>{enabled ? "No priced lots yet." : "Disabled."}</Hint>;
  }
  return <LotTable lots={snap.data.lots.slice(0, 8)} />;
}

const bidrl: PluginModule = {
  id: "bidrl",
  nav: [{ path: "/bidrl", label: "BIDRL" }],
  routes: [
    { path: "/bidrl", element: <Feed /> },
    { path: "/bidrl/auction/:id", element: <AuctionView /> },
    { path: "/bidrl/lot/:id", element: <LotView /> },
  ],
  dashboard: {
    summary: "Searches SITES auctions first, then scores lots from photographs.",
    live: ["bidrl.**"],
    tile: Tile,
    detail: Detail,
  },
};

export default bidrl;
