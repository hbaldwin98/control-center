/** Every read this plugin makes, as one hook per endpoint. */
import {
  useCallback,
  useEffect,
  useMemo,
  useState,
} from "react";
import {
  useSnapshot,
} from "@cc/ui";
import type { Snapshot, UseSnapshotResult } from "@cc/ui";
import {
  eventBoundary,
  type AuctionPage,
  type AutomationPage,
  type AuctionIndex,
  type AuctionsPage,
  type FavoritesPage,
  type FeedPage,
  type FindingsPage,
  type IntentPage,
  type LocationsPage,
  type Lot,
  type LotsPage,
  type OverviewPage,
  type SitesPage,
  type WatchlistsPage,
} from "./model";
import { api } from "./api";

export const LOT_PAGE_SIZE = 50;

type LotPageResponse = {
  lots: Lot[];
  latestEventId: number;
  page?: number;
  perPage?: number;
  total?: number;
  totalPages?: number;
  hasNext?: boolean;
};

type TaggedPage<T extends LotPageResponse> = T & {
  __requestKey: string;
  __requestPage: number;
};

type PageLoader<T extends LotPageResponse> = (
  page: number,
  signal: AbortSignal,
) => Promise<Snapshot<T>>;

export type InfiniteLotsResult<T extends LotPageResponse = LotPageResponse> = {
  status: "loading" | "ready" | "error";
  lots: Lot[];
  latest: T | null;
  total: number;
  totalPages: number;
  hasMore: boolean;
  loadingMore: boolean;
  error: Error | null;
  loadMore: () => void;
  reload: () => void;
};

/**
 * Loads bounded API pages while retaining the rows already scrolled past. The current
 * page remains a normal useSnapshot, so stream invalidation still refreshes it; replacing
 * that page in the map avoids duplicating rows after a live event.
 */
export function useInfiniteLotPages<T extends LotPageResponse>(
  key: string,
  loadPage: PageLoader<T>,
): InfiniteLotsResult<T> {
  const [page, setPage] = useState(1);
  const [loadedPage, setLoadedPage] = useState(0);
  const [pageRows, setPageRows] = useState<Record<number, Lot[]>>({});
  const [total, setTotal] = useState(0);
  const [totalPages, setTotalPages] = useState(0);
  const [latest, setLatest] = useState<T | null>(null);

  const load = useCallback(async (signal: AbortSignal) => {
    const snap = await loadPage(page, signal);
    return {
      ...snap,
      data: {
        ...snap.data,
        __requestKey: key,
        __requestPage: page,
      },
    };
  }, [key, loadPage, page]);
  const snap = useSnapshot<TaggedPage<T>>(load, { events: "bidrl.**" });
  const snapData = snap.status === "ready" ? snap.data : null;

  useEffect(() => {
    setPage(1);
    setLoadedPage(0);
    setPageRows({});
    setTotal(0);
    setTotalPages(0);
    setLatest(null);
  }, [key]);

  useEffect(() => {
    if (
      snapData === null ||
      snapData.__requestKey !== key ||
      snapData.__requestPage !== page
    ) {
      return;
    }
    const data = snapData;
    const size = data.perPage ?? LOT_PAGE_SIZE;
    const nextTotal = data.total ?? (data.hasNext ? page * size + 1 : data.lots.length);
    const nextTotalPages = data.totalPages ?? (data.hasNext ? page + 1 : page);
    setPageRows((current) => ({ ...current, [page]: data.lots }));
    setLoadedPage((current) => Math.max(current, page));
    setTotal(nextTotal);
    setTotalPages(nextTotalPages);
    setLatest(data);
  }, [key, page, snapData]);

  const lots = useMemo(() => {
    const seen = new Set<string>();
    const out: Lot[] = [];
    for (const pageNumber of Object.keys(pageRows).map(Number).sort((a, b) => a - b)) {
      for (const lot of pageRows[pageNumber] ?? []) {
        if (seen.has(lot.id)) continue;
        seen.add(lot.id);
        out.push(lot);
      }
    }
    return out;
  }, [pageRows]);

  const current = snap.status === "ready" &&
    snap.data.__requestKey === key && snap.data.__requestPage === page;
  const status: InfiniteLotsResult<T>["status"] =
    lots.length > 0 || current ? "ready" : snap.status;
  const loadingMore = loadedPage > 0 && page > loadedPage && snap.status !== "error";
  const hasMore = loadedPage > 0 && loadedPage < totalPages;
  const loadMore = useCallback(() => {
    if (snap.status === "error") {
      snap.reload();
      return;
    }
    if (loadingMore || !hasMore) return;
    setPage(loadedPage + 1);
  }, [hasMore, loadedPage, loadingMore, snap.reload, snap.status]);
  const reload = useCallback(() => {
    setPage(1);
    setLoadedPage(0);
    setPageRows({});
    setTotal(0);
    setTotalPages(0);
    setLatest(null);
    snap.reload();
  }, [snap.reload]);

  return {
    status,
    lots,
    latest,
    total,
    totalPages,
    hasMore,
    loadingMore,
    error: snap.status === "error" ? snap.error : null,
    loadMore,
    reload,
  };
}

export function useInfiniteLots(
  filter: string,
  q: string,
  bucket: string,
  category: string,
  ending: string,
  affiliate: string,
): InfiniteLotsResult {
  const key = ["lots", filter, q, bucket, category, ending, affiliate].join("\u0000");
  const loadPage = useCallback(async (page: number, signal: AbortSignal) => {
    const params = new URLSearchParams({ page: String(page), perPage: String(LOT_PAGE_SIZE) });
    if (filter) params.set("filter", filter);
    if (q.trim()) params.set("q", q.trim());
    if (bucket && bucket !== "all") params.set("bucket", bucket);
    if (category && category !== "all") params.set("category", category);
    if (ending) params.set("ending", ending);
    if (affiliate) params.set("affiliate", affiliate);
    const data = await api.get<LotsPage>(`/lots?${params}`, signal);
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, [affiliate, bucket, category, ending, filter, q]);
  return useInfiniteLotPages(key, loadPage);
}

export function useInfiniteFavorites(
  q: string,
  category: string,
  affiliate: string,
): InfiniteLotsResult {
  const key = ["favorites", q, category, affiliate].join("\u0000");
  const loadPage = useCallback(async (page: number, signal: AbortSignal) => {
    const params = new URLSearchParams({ page: String(page), perPage: String(LOT_PAGE_SIZE) });
    if (q.trim()) params.set("q", q.trim());
    if (category && category !== "all") params.set("category", category);
    if (affiliate) params.set("affiliate", affiliate);
    const data = await api.get<FavoritesPage>(`/favorites?${params}`, signal);
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, [affiliate, category, q]);
  return useInfiniteLotPages(key, loadPage);
}

export function useInfiniteAuction(id: string): InfiniteLotsResult<AuctionPage> {
  const key = ["auction", id].join("\u0000");
  const loadPage = useCallback(async (page: number, signal: AbortSignal) => {
    const params = new URLSearchParams({ page: String(page), perPage: String(LOT_PAGE_SIZE) });
    const data = await api.get<AuctionPage>(`/auctions/${encodeURIComponent(id)}?${params}`, signal);
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, [id]);
  return useInfiniteLotPages(key, loadPage);
}

export function useFeed(filter: string, q: string): UseSnapshotResult<FeedPage> {
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

export function useAuctions(): UseSnapshotResult<AuctionsPage> {
  const load = useCallback(async (signal: AbortSignal) => {
    const data = await api.get<AuctionsPage>("/auctions", signal);
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, []);
  return useSnapshot(load, { events: "bidrl.**" });
}

export function useAuction(id: string): UseSnapshotResult<AuctionPage> {
  const load = useCallback(async (signal: AbortSignal) => {
    const data = await api.get<AuctionPage>(`/auctions/${encodeURIComponent(id)}`, signal);
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, [id]);
  return useSnapshot(load, { events: "bidrl.**" });
}

export function useLot(id: string): UseSnapshotResult<Lot> {
  const load = useCallback(async (signal: AbortSignal) => {
    const data = await api.get<Lot>(`/lots/${encodeURIComponent(id)}`, signal);
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, [id]);
  return useSnapshot(load, { events: "bidrl.**" });
}

export function useSites(): UseSnapshotResult<SitesPage> {
  const load = useCallback(async (signal: AbortSignal) => {
    const data = await api.get<SitesPage>("/sites/auctions", signal);
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, []);
  return useSnapshot(load, { events: "bidrl.**" });
}

export function useLots(
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

export function useFavorites(
  q: string,
  category: string,
  affiliate: string,
): UseSnapshotResult<FavoritesPage> {
  const load = useCallback(async (signal: AbortSignal) => {
    const params = new URLSearchParams();
    if (q.trim()) params.set("q", q.trim());
    if (category && category !== "all") params.set("category", category);
    if (affiliate) params.set("affiliate", affiliate);
    const qs = params.toString();
    const data = await api.get<FavoritesPage>(`/favorites${qs ? `?${qs}` : ""}`, signal);
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, [q, category, affiliate]);
  return useSnapshot(load, { events: "bidrl.**" });
}

export function useFindings(state: string, watchlist: string): UseSnapshotResult<FindingsPage> {
  const load = useCallback(async (signal: AbortSignal) => {
    const params = new URLSearchParams();
    if (state) params.set("state", state);
    if (watchlist) params.set("watchlist", watchlist);
    const qs = params.toString();
    const data = await api.get<FindingsPage>(`/findings${qs ? `?${qs}` : ""}`, signal);
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, [state, watchlist]);
  return useSnapshot(load, { events: "bidrl.**" });
}

export function useAutomation(): UseSnapshotResult<AutomationPage> {
  const load = useCallback(async (signal: AbortSignal) => {
    const data = await api.get<AutomationPage>("/automation", signal);
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, []);
  return useSnapshot(load, { events: "bidrl.**" });
}

export function useWatchlists(): UseSnapshotResult<WatchlistsPage> {
  const load = useCallback(async (signal: AbortSignal) => {
    const data = await api.get<WatchlistsPage>("/watchlists", signal);
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, []);
  return useSnapshot(load, { events: "bidrl.**" });
}

/**
 * The locations you have lots at. Its own request, not derived from the lot list:
 * deriving it would delete every unselected location from the filter the moment
 * you picked one.
 */
export function useLocations(): UseSnapshotResult<LocationsPage> {
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
export function useAuctionLots(auctionId: string): AuctionIndex {
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

export function useOverview(): UseSnapshotResult<OverviewPage> {
  const load = useCallback(async (signal: AbortSignal) => {
    const data = await api.get<OverviewPage>("/overview", signal);
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, []);
  return useSnapshot(load, { events: "bidrl.**" });
}

export function useIntent(): UseSnapshotResult<IntentPage> {
  const load = useCallback(async (signal: AbortSignal) => {
    const data = await api.get<IntentPage>("/intent", signal);
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, []);
  return useSnapshot(load, { events: "bidrl.**" });
}

export function useLotView(): ["grid" | "table", (view: "grid" | "table") => void] {
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
