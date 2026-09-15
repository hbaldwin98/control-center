import {
  useEffect,
  useState,
} from "react";
import {
  Button,
  Callout,
  Card,
  Checkbox,
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
  LOT_PRESETS,
  affiliateParam,
  filterLabel,
  locationLabel,
  parseAffiliateParam,
  overlayBids,
} from "../model";
import { usePlace } from "../place";
import { useInfiniteLots, useLocations, useLotView } from "../data";
import { useLiveBids, LiveDot } from "../live";
import { ViewToggle, BidrlTabs } from "../chrome";
import { Notices } from "../actions";
import { LotBrowser, LotLoadMore } from "../lots";

/**
 * The one catalog. Its whole state — preset, text, bucket, category, ending, locations — lives in
 * the query string, so a filtered list can be linked to, and going into a lot and back
 * returns the list you left rather than a reset one.
 */
export function LotsCatalog() {
  const [filter, setFilter] = useQueryState("filter");
  const [q, setQ] = useQueryState("q");
  const [bucket, setBucket] = useQueryState("bucket", "all");
  const [category, setCategory] = useQueryState("category", "all");
  const [ending, setEnding] = useQueryState("ending");
  const [affiliate, setAffiliate] = useQueryState("affiliate");
  const [draft, setDraft] = useState(q);
  const [view, setView] = useLotView();
  const snap = useInfiniteLots(filter, q, bucket, category, ending, affiliate);
  const locations = useLocations();
  const disabled = snap.error instanceof PluginDisabledError;
  const live = useLiveBids(snap.status === "ready" ? snap.lots : undefined, !disabled);
  const selected = parseAffiliateParam(affiliate);
  const narrowed =
    Boolean(filter || q || ending || affiliate) || bucket !== "all" || category !== "all";
  usePlace("/bidrl/lots", snap.status === "ready");

  const toggleLocation = (id: string) => {
    setAffiliate(
      affiliateParam(selected.includes(id) ? selected.filter((x) => x !== id) : [...selected, id]),
    );
  };

  // A query typed on another screen, or arrived at by link, has to show in the box.
  useEffect(() => setDraft(q), [q]);

  const clearAll = () => {
    setDraft("");
    setQ("");
    setFilter("");
    setBucket("all");
    setCategory("all");
    setEnding("");
    setAffiliate("");
  };

  return (
    <Page>
      <PageHeader
        title="Lots"
        lede="Every collected lot. Start from a preset, then narrow by text, bucket, category, or the locations you can actually drive to. Looking for something by purpose rather than by word? Ask on the Intent tab."
        actions={<LiveDot status={live.status} />}
      />
      <Stack>
        <BidrlTabs />
        <Notices message={null} error={null} disabled={disabled} />
        <Card
          title="Catalog"
          actions={
            <>
              {narrowed ? (
                <Button size="sm" onClick={clearAll}>
                  Clear filters
                </Button>
              ) : null}
              <ViewToggle value={view} onChange={setView} />
            </>
          }
        >
          <Toolbar>
            <Field label="Show">
              <Select value={filter} onChange={(e) => setFilter(e.target.value)} aria-label="Preset">
                {LOT_PRESETS.map((preset) => (
                  <option key={preset.value} value={preset.value}>{preset.label}</option>
                ))}
              </Select>
            </Field>
            <Field label="Find">
              <Input
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") setQ(draft);
                }}
                placeholder="Title, identification, model, category…"
                disabled={disabled}
                aria-label="Lot search"
              />
            </Field>
            <Button disabled={disabled} onClick={() => setQ(draft)}>
              Find
            </Button>
            <Field label="Bucket">
              <Select value={bucket} onChange={(e) => setBucket(e.target.value)} aria-label="Bucket">
                <option value="all">All buckets</option>
                <option value="priced">Priced</option>
                <option value="worth_opening">Worth opening</option>
                <option value="research">Research</option>
                <option value="skipped">Skipped</option>
                <option value="discarded">Discarded</option>
                <option value="pending">Pending</option>
              </Select>
            </Field>
            <Field label="Category">
              <Select value={category} onChange={(e) => setCategory(e.target.value)} aria-label="Category">
                <option value="all">All categories</option>
                {LOT_CATEGORIES.map((c) => (
                  <option key={c} value={c}>{c}</option>
                ))}
              </Select>
            </Field>
            <Checkbox
              label="Ending soon"
              checked={ending === "soon"}
              onChange={(e) => setEnding(e.target.checked ? "soon" : "")}
              disabled={disabled}
            />
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
          <Hint>
            {filterLabel(filter)}
            {snap.status === "ready" ? ` · ${snap.lots.length}${snap.hasMore ? " loaded" : " shown"}` : ""}
          </Hint>
          {snap.status === "loading" ? <Loading label="Loading lots…" /> : null}
          {snap.status === "error" && !disabled ? <Callout tone="danger">{snap.error?.message ?? "Could not load lots."}</Callout> : null}
          {snap.status === "ready" ? (
            <>
              <LotBrowser
                lots={overlayBids(snap.lots, live.bids)}
                empty={
                  narrowed
                    ? "No lot matches these filters. Clear them to see the whole catalog."
                    : "No lots collected yet. Collect an auction on the Auctions tab."
                }
                view={view}
              />
              <LotLoadMore
                hasMore={snap.hasMore}
                loading={snap.loadingMore}
                onLoadMore={snap.loadMore}
              />
            </>
          ) : null}
        </Card>
      </Stack>
    </Page>
  );
}
