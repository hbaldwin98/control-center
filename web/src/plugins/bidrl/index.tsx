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
  const [url, setUrl] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const disabled = snap.error instanceof PluginDisabledError;

  const add = async () => {
    setBusy(true);
    setError(null);
    setMessage(null);
    try {
      const result = await api.post<{ jobId: number; auctionId: string }>("/auctions", { url });
      setMessage(`Collect queued as job ${result.jobId}.`);
      setUrl("");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Page>
      <PageHeader
        title="BIDRL"
        lede="Lots scored from photographs. A number appears only when a model or barcode is cited."
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
                disabled={disabled || busy}
                aria-label="Auction URL"
              />
            </Field>
            <Button variant="primary" disabled={disabled || busy || url.trim() === ""} onClick={() => void add()}>
              {busy ? "Queueing…" : "Add auction"}
            </Button>
          </Stack>
        </Card>
        {auctions.status === "ready" && auctions.data.auctions.length > 0 ? (
          <Card title="Auctions">
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
    summary: "Scores BIDRL lots from photographs and ranks cited deals.",
    live: ["bidrl.**"],
    tile: Tile,
    detail: Detail,
  },
};

export default bidrl;
