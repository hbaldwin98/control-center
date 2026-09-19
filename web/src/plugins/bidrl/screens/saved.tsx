import { useEffect, useState } from "react";
import {
  Button,
  Callout,
  Card,
  Field,
  Hint,
  Input,
  Loading,
  Page,
  PageHeader,
  PluginDisabledError,
  Select,
  Stack,
  Toolbar,
  useQueryState,
} from "@cc/ui";
import {
  LOT_CATEGORIES,
  LOT_SORT_DEFAULTS,
  affiliateParam,
  cycleSort,
  locationLabel,
  lotSortParam,
  parseAffiliateParam,
  parseLotSort,
  overlayBids,
  type LotSortColumn,
} from "../model";
import { usePlace } from "../place";
import { useInfiniteFavorites, useLocations, useLotView } from "../data";
import { useLiveBids, LiveDot } from "../live";
import { ViewToggle, BidrlTabs } from "../chrome";
import { Notices } from "../actions";
import { FavoriteChanged } from "../lotparts";
import { LotBrowser, LotLoadMore, LotRefreshIndicator } from "../lots";

/**
 * Saved lots. The same card/table browser and the same location and category filters as
 * the catalog, because it is the same kind of list — what differs is that you chose
 * every row on it, so it is sorted by when you saved rather than by lot id, and the note
 * you left is part of the row.
 *
 * "Remove ended" never deletes a saved lot, so this list keeps working after the auction
 * closes; that is the point of saving something.
 */
export function SavedLots() {
  const [q, setQ] = useQueryState("q");
  const [category, setCategory] = useQueryState("category", "all");
  const [affiliate, setAffiliate] = useQueryState("affiliate");
  const [sortParam, setSortParam] = useQueryState("sort");
  const [draft, setDraft] = useState(q);
  const [view, setView] = useLotView();
  const [advancedOpen, setAdvancedOpen] = useState(
    () => typeof window === "undefined" || window.innerWidth > 700,
  );
  const sort = parseLotSort(sortParam, { column: "saved", dir: "desc" });
  const snap = useInfiniteFavorites(q, category, affiliate, lotSortParam(sort));
  const locations = useLocations();
  const disabled = snap.error instanceof PluginDisabledError;
  const live = useLiveBids(
    snap.status === "ready" ? snap.lots : undefined,
    !disabled,
  );
  const selected = parseAffiliateParam(affiliate);
  const narrowed = Boolean(q || affiliate) || category !== "all";
  const advancedCount = [category !== "all", selected.length > 0].filter(
    Boolean,
  ).length;
  usePlace("/bidrl/saved", snap.status === "ready");

  const changeSort = (column: LotSortColumn) => {
    const next = cycleSort(sort, column, LOT_SORT_DEFAULTS[column]);
    setSortParam(next ? lotSortParam(next) : "");
  };

  useEffect(() => setDraft(q), [q]);

  const toggleLocation = (id: string) => {
    setAffiliate(
      affiliateParam(
        selected.includes(id)
          ? selected.filter((x) => x !== id)
          : [...selected, id],
      ),
    );
  };

  return (
    <Page>
      <PageHeader
        eyebrow="BidRL / Lots"
        title="Saved"
        lede="Your starred lots, with photos and comparables kept after auctions close."
        actions={<LiveDot status={live.status} />}
      />
      <Stack>
        <BidrlTabs />
        <Notices message={null} error={null} disabled={disabled} />
        <Card
          title="Saved lots"
          className="bidrl-surface bidrl-saved-surface"
          actions={
            <>
              {narrowed ? (
                <Button
                  size="sm"
                  onClick={() => {
                    setDraft("");
                    setQ("");
                    setCategory("all");
                    setAffiliate("");
                    setSortParam("");
                  }}
                >
                  Clear filters
                </Button>
              ) : null}
              <ViewToggle value={view} onChange={setView} />
            </>
          }
        >
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
                aria-label="Saved search"
              />
            </Field>
            <Button disabled={disabled} onClick={() => setQ(draft)}>
              Find
            </Button>
            <Field label="Sort">
              <Select
                value={lotSortParam(sort)}
                onChange={(e) => setSortParam(e.target.value)}
                aria-label="Sort saved lots"
              >
                <option value="saved.desc">Recently saved</option>
                <option value="ends.asc">Closing soon</option>
                <option value="gap.desc">Best opportunities</option>
                <option value="bid.asc">Lowest bid</option>
                <option value="name.asc">Name</option>
              </Select>
            </Field>
          </Toolbar>
          <details
            className="bidrl-advanced-filters"
            open={advancedOpen}
            onToggle={(event) => setAdvancedOpen(event.currentTarget.open)}
          >
            <summary>
              More filters
              {advancedCount > 0 ? ` · ${advancedCount} active` : ""}
            </summary>
            <div className="bidrl-advanced-filters__body">
              <Toolbar>
                <Field label="Category">
                  <Select
                    value={category}
                    onChange={(e) => setCategory(e.target.value)}
                    aria-label="Category"
                  >
                    <option value="all">All categories</option>
                    {LOT_CATEGORIES.map((c) => (
                      <option key={c} value={c}>
                        {c}
                      </option>
                    ))}
                  </Select>
                </Field>
              </Toolbar>
              {locations.status === "ready" &&
              locations.data.locations.length > 0 ? (
                <Field label="Locations">
                  <div className="bidrl-loc-filter">
                    {locations.data.locations.map((loc) => (
                      <Button
                        key={loc.id}
                        size="sm"
                        pressed={selected.includes(loc.id)}
                        disabled={disabled}
                        onClick={() => toggleLocation(loc.id)}
                      >
                        {locationLabel(loc)} · {loc.lotCount}
                      </Button>
                    ))}
                    {selected.length > 0 ? (
                      <Button size="sm" onClick={() => setAffiliate("")}>
                        All locations
                      </Button>
                    ) : null}
                  </div>
                </Field>
              ) : null}
            </div>
          </details>
          {snap.status === "ready" ? (
            <Hint>
              {snap.hasMore
                ? `${snap.lots.length} of ${snap.total} loaded`
                : `${snap.lots.length} saved`}
            </Hint>
          ) : null}
          {snap.status === "loading" ? (
            <Loading label="Loading saved lots…" />
          ) : null}
          {snap.error && !disabled ? (
            <Callout tone="danger">
              {snap.error.message || "Could not load saved lots."}
            </Callout>
          ) : null}
          {snap.status === "ready" ? (
            // Unstarring a row takes it off this list, so the list reloads the moment a
            // star changes rather than waiting for a refresh to notice.
            <FavoriteChanged.Provider value={snap.reload}>
              <>
                <div className="bidrl-lot-results">
                  <LotRefreshIndicator refreshing={snap.refreshing} />
                  <LotBrowser
                    lots={overlayBids(snap.lots, live.bids)}
                    empty={
                      narrowed
                        ? "No saved lot matches these filters."
                        : "Nothing saved yet. Star a lot anywhere — the catalog, an auction, or its own page — to keep it here."
                    }
                    view={view}
                    // Every row here was chosen on purpose, so two similar lots must both
                    // show rather than collapsing into "1 similar".
                    groupSimilar={false}
                    sort={snap.refreshing ? null : sort}
                    onSort={changeSort}
                  />
                </div>
                <LotLoadMore
                  hasMore={snap.hasMore}
                  loading={snap.loadingMore}
                  onLoadMore={snap.loadMore}
                />
              </>
            </FavoriteChanged.Provider>
          ) : null}
        </Card>
      </Stack>
    </Page>
  );
}
