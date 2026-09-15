/** Every read this plugin makes, as one hook per endpoint. */
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
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
  /** True while a new filter/sort key is fetching; existing rows remain visible. */
  refreshing: boolean;
  error: Error | null;
  loadMore: () => void;
  reload: () => void;
};

/**
 * Loads bounded API pages while retaining the rows already scrolled past. The current
 * page remains a normal useSnapshot, so stream invalidation still refreshes it; replacing
 * that page in the map avoids duplicating rows after a live event. A sort/filter key keeps
 * the old map until page one of the new query arrives, so a refresh never flashes empty.
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
  const [refreshing, setRefreshing] = useState(false);
  // Keep the previous rows on screen while a new query key is in flight. The refs also
  // let the first render after a sort request page 1, even if the old list had loaded
  // several pages already.
  const displayedKey = useRef(key);
  const replaceOnNextPage = useRef(false);
  const requestPage = displayedKey.current === key && !replaceOnNextPage.current ? page : 1;

  const load = useCallback(async (signal: AbortSignal) => {
    try {
      const snap = await loadPage(requestPage, signal);
      return {
        ...snap,
        data: {
          ...snap.data,
          __requestKey: key,
          __requestPage: requestPage,
        },
      };
    } catch (error) {
      if (!signal.aborted) {
        // A failed refresh should stop saying "updating" while the old rows remain
        // available, and the screen can expose the error without flashing empty state.
        displayedKey.current = key;
        replaceOnNextPage.current = false;
        setRefreshing(false);
      }
      throw error;
    }
  }, [key, loadPage, requestPage]);
  const snap = useSnapshot<TaggedPage<T>>(load, { events: "bidrl.**" });
  const snapData = snap.status === "ready" ? snap.data : null;

  useEffect(() => {
    if (displayedKey.current === key) return;
    replaceOnNextPage.current = true;
    setRefreshing(true);
    setPage(1);
    setLoadedPage(0);
    setTotalPages(0);
  }, [key]);

  useEffect(() => {
    if (
      snapData === null ||
      snapData.__requestKey !== key ||
      snapData.__requestPage !== requestPage
    ) {
      return;
    }
    const data = snapData;
    const replacing = replaceOnNextPage.current || displayedKey.current !== key;
    displayedKey.current = key;
    replaceOnNextPage.current = false;
    const size = data.perPage ?? LOT_PAGE_SIZE;
    const nextTotal = data.total ?? (data.hasNext ? requestPage * size + 1 : data.lots.length);
    const nextTotalPages = data.totalPages ?? (data.hasNext ? requestPage + 1 : requestPage);
    setPageRows((current) =>
      replacing ? { [requestPage]: data.lots } : { ...current, [requestPage]: data.lots },
    );
    setLoadedPage((current) =>
      replacing ? requestPage : Math.max(current, requestPage),
    );
    setTotal(nextTotal);
    setTotalPages(nextTotalPages);
    setLatest(data);
    setRefreshing(false);
  }, [key, requestPage, snapData]);

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
    snap.data.__requestKey === key && snap.data.__requestPage === requestPage;
  const isRefreshing = refreshing || displayedKey.current !== key || replaceOnNextPage.current;
  const status: InfiniteLotsResult<T>["status"] =
    lots.length > 0 || current ? "ready" : snap.status;
  const loadingMore = !isRefreshing && loadedPage > 0 && page > loadedPage && snap.status !== "error";
  const hasMore = !isRefreshing && loadedPage > 0 && loadedPage < totalPages;
  const loadMore = useCallback(() => {
    if (isRefreshing) return;
    if (snap.status === "error") {
      snap.reload();
      return;
    }
    if (loadingMore || !hasMore) return;
    setPage(loadedPage + 1);
  }, [hasMore, isRefreshing, loadedPage, loadingMore, snap.reload, snap.status]);
  const reload = useCallback(() => {
    replaceOnNextPage.current = true;
    setRefreshing(true);
    setPage(1);
    setLoadedPage(0);
    setTotalPages(0);
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
    refreshing: isRefreshing,
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
  sort: string,
): InfiniteLotsResult {
  const key = ["lots", filter, q, bucket, category, ending, affiliate, sort].join("\u0000");
  const loadPage = useCallback(async (page: number, signal: AbortSignal) => {
    const params = new URLSearchParams({ page: String(page), perPage: String(LOT_PAGE_SIZE), sort });
    if (filter) params.set("filter", filter);
    if (q.trim()) params.set("q", q.trim());
    if (bucket && bucket !== "all") params.set("bucket", bucket);
    if (category && category !== "all") params.set("category", category);
    if (ending) params.set("ending", ending);
    if (affiliate) params.set("affiliate", affiliate);
    const data = await api.get<LotsPage>(`/lots?${params}`, signal);
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, [affiliate, bucket, category, ending, filter, q, sort]);
  return useInfiniteLotPages(key, loadPage);
}

export function useInfiniteFavorites(
  q: string,
  category: string,
  affiliate: string,
  sort: string,
): InfiniteLotsResult {
  const key = ["favorites", q, category, affiliate, sort].join("\u0000");
  const loadPage = useCallback(async (page: number, signal: AbortSignal) => {
    const params = new URLSearchParams({ page: String(page), perPage: String(LOT_PAGE_SIZE), sort });
    if (q.trim()) params.set("q", q.trim());
    if (category && category !== "all") params.set("category", category);
    if (affiliate) params.set("affiliate", affiliate);
    const data = await api.get<FavoritesPage>(`/favorites?${params}`, signal);
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, [affiliate, category, q, sort]);
  return useInfiniteLotPages(key, loadPage);
}

export function useInfiniteAuction(id: string, sort: string): InfiniteLotsResult<AuctionPage> {
  const key = ["auction", id, sort].join("\u0000");
  const loadPage = useCallback(async (page: number, signal: AbortSignal) => {
    const params = new URLSearchParams({ page: String(page), perPage: String(LOT_PAGE_SIZE), sort });
    const data = await api.get<AuctionPage>(`/auctions/${encodeURIComponent(id)}?${params}`, signal);
    return { data, asOfEventId: eventBoundary(data.latestEventId) };
  }, [id, sort]);
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
