import {
  useEffect,
  useState,
} from "react";
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
  affiliateParam,
  locationLabel,
  parseAffiliateParam,
  overlayBids,
} from "../model";
import { usePlace } from "../place";
import { useInfiniteFavorites, useLocations, useLotView } from "../data";
import { useLiveBids, LiveDot } from "../live";
import { ViewToggle, BidrlTabs } from "../chrome";
import { Notices } from "../actions";
import { FavoriteChanged } from "../lotparts";
import { LotBrowser, LotLoadMore } from "../lots";

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
  const [draft, setDraft] = useState(q);
  const [view, setView] = useLotView();
  const snap = useInfiniteFavorites(q, category, affiliate);
  const locations = useLocations();
  const disabled = snap.error instanceof PluginDisabledError;
  const live = useLiveBids(snap.status === "ready" ? snap.lots : undefined, !disabled);
  const selected = parseAffiliateParam(affiliate);
  const narrowed = Boolean(q || affiliate) || category !== "all";
  usePlace("/bidrl/saved", snap.status === "ready");

  useEffect(() => setDraft(q), [q]);

  const toggleLocation = (id: string) => {
    setAffiliate(
      affiliateParam(selected.includes(id) ? selected.filter((x) => x !== id) : [...selected, id]),
    );
  };

  return (
    <Page>
      <PageHeader
        title="Saved"
        lede="Lots you starred, newest first. Nothing here is removed by “Remove ended” — a saved lot keeps its photos, comparable, and location after the auction closes."
        actions={<LiveDot status={live.status} />}
      />
      <Stack>
        <BidrlTabs />
        <Notices message={null} error={null} disabled={disabled} />
        <Card
          title="Saved lots"
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
            <Field label="Category">
              <Select
                value={category}
                onChange={(e) => setCategory(e.target.value)}
                aria-label="Category"
              >
                <option value="all">All categories</option>
                {LOT_CATEGORIES.map((c) => (
                  <option key={c} value={c}>{c}</option>
                ))}
              </Select>
            </Field>
          </Toolbar>
          {locations.status === "ready" && locations.data.locations.length > 0 ? (
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
          {snap.status === "ready" ? (
            <Hint>{snap.lots.length}{snap.hasMore ? " loaded" : " saved"}</Hint>
          ) : null}
          {snap.status === "loading" ? <Loading label="Loading saved lots…" /> : null}
          {snap.status === "error" && !disabled ? (
            <Callout tone="danger">{snap.error?.message ?? "Could not load saved lots."}</Callout>
          ) : null}
          {snap.status === "ready" ? (
            // Unstarring a row takes it off this list, so the list reloads the moment a
            // star changes rather than waiting for a refresh to notice.
            <FavoriteChanged.Provider value={snap.reload}>
              <>
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
                />
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
