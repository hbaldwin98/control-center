/** Every read this plugin makes, as one hook per endpoint. */
import {
  useCallback,
  useState,
} from "react";
import {
  useSnapshot,
} from "@cc/ui";
import type { UseSnapshotResult } from "@cc/ui";
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
