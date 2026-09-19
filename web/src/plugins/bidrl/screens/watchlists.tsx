import {
  useMemo,
  useState,
} from "react";
import {
  Badge,
  Button,
  Callout,
  Card,
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
  Toolbar,
} from "@cc/ui";
import {
  LOT_CATEGORIES,
  locationLabel,
  watchlistRules,
} from "../model";
import { api } from "../api";
import { useWatchlists, useLocations } from "../data";
import { BidrlTabs } from "../chrome";
import { Notices } from "../actions";

/**
 * Watchlists: a description of what you want, plus the rules that keep the queue short.
 * Running one is a job — it embeds, judges, and may read photographs — so the button
 * says what it queued rather than pretending the work is done.
 */
export function Watchlists() {
  const snap = useWatchlists();
  const locations = useLocations();
  const [name, setName] = useState("");
  const [query, setQuery] = useState("");
  const [maxBid, setMaxBid] = useState("");
  const [affiliates, setAffiliates] = useState<string[]>([]);
  const [categories, setCategories] = useState<string[]>([]);
  const [notice, setNotice] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const disabled = snap.error instanceof PluginDisabledError;

  const labels = useMemo(() => {
    const map = new Map<string, string>();
    if (locations.status === "ready") {
      for (const loc of locations.data.locations) map.set(loc.id, locationLabel(loc));
    }
    return map;
  }, [locations]);

  const toggle = (list: string[], set: (next: string[]) => void, value: string) => {
    set(list.includes(value) ? list.filter((x) => x !== value) : [...list, value]);
  };

  const create = async () => {
    const q = query.trim();
    if (!q) return;
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      const dollars = Number.parseFloat(maxBid);
      await api.post("/watchlists", {
        name: name.trim(),
        query: q,
        affiliateIds: affiliates,
        categories,
        maxBidCents: Number.isFinite(dollars) && dollars > 0 ? Math.round(dollars * 100) : null,
      });
      setName("");
      setQuery("");
      setMaxBid("");
      setAffiliates([]);
      setCategories([]);
      setNotice("Watchlist saved. Run it to look through what you have collected.");
      snap.reload();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not save that watchlist.");
    } finally {
      setBusy(false);
    }
  };

  const act = async (fn: () => Promise<unknown>, message: string) => {
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      await fn();
      setNotice(message);
      snap.reload();
    } catch (e) {
      setError(e instanceof Error ? e.message : "That did not work.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Page>
      <PageHeader
        eyebrow="BidRL"
        title="Watchlists"
        lede="Describe what you are after and how far you would drive for it. Running one costs a little: it ranks your collected lots locally, asks a cheap model about the few that survive, and only then reads photographs."
      />
      <Stack>
        <BidrlTabs />
        <Notices message={notice} error={error} disabled={disabled} />
        <Card title="New watchlist" className="bidrl-surface bidrl-watchlists-form">
          <Stack>
            <Toolbar>
              <Field label="Name">
                <Input
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="Camping"
                  aria-label="Watchlist name"
                  disabled={disabled}
                />
              </Field>
              <Field label="Looking for">
                <Input
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") void create();
                  }}
                  placeholder="camping gear — tent, stove, sleeping bag"
                  aria-label="Watchlist query"
                  disabled={disabled}
                />
              </Field>
              <Field label="Max bid ($)">
                <Input
                  value={maxBid}
                  onChange={(e) => setMaxBid(e.target.value)}
                  placeholder="80"
                  inputMode="decimal"
                  aria-label="Max bid"
                  disabled={disabled}
                />
              </Field>
            </Toolbar>
            {locations.status === "ready" && locations.data.locations.length > 0 ? (
              <Field label="Locations">
                <div className="bidrl-loc-filter">
                  {locations.data.locations.map((loc) => (
                    <Button
                      key={loc.id}
                      size="sm"
                      pressed={affiliates.includes(loc.id)}
                      disabled={disabled}
                      onClick={() => toggle(affiliates, setAffiliates, loc.id)}
                    >
                      {locationLabel(loc)}
                    </Button>
                  ))}
                </div>
              </Field>
            ) : null}
            <Field label="Categories">
              <div className="bidrl-loc-filter">
                {LOT_CATEGORIES.map((c) => (
                  <Button
                    key={c}
                    size="sm"
                    pressed={categories.includes(c)}
                    disabled={disabled}
                    onClick={() => toggle(categories, setCategories, c)}
                  >
                    {c}
                  </Button>
                ))}
              </div>
            </Field>
            <Hint>
              Leaving locations or categories unchosen means every one of them. A category
              only narrows lots a scan has already looked at.
            </Hint>
            <div className="bidrl-actions">
              <Button variant="primary" disabled={disabled || busy || !query.trim()} onClick={() => void create()}>
                Save watchlist
              </Button>
            </div>
          </Stack>
        </Card>
        <Card title="Your watchlists" className="bidrl-surface bidrl-watchlists-surface">
          {snap.status === "loading" ? <Loading label="Loading watchlists…" /> : null}
          {snap.status === "ready" && snap.data.watchlists.length === 0 ? (
            <EmptyState>Nothing watched yet.</EmptyState>
          ) : null}
          {snap.status === "ready"
            ? snap.data.watchlists.map((w) => (
                <div key={w.id} className="bidrl-watchlist">
                  <div className="bidrl-watchlist__body">
                    <strong>{w.name}</strong>
                    {w.enabled ? null : <Badge>paused</Badge>}
                    {w.newFindings > 0 ? (
                      <Link to={`/bidrl/findings?watchlist=${encodeURIComponent(w.id)}`}>
                        <Badge tone="ok">{w.newFindings} to review</Badge>
                      </Link>
                    ) : null}
                    <Hint>{w.query}</Hint>
                    <Hint>{watchlistRules(w, labels)}</Hint>
                    {w.lastError ? <Callout tone="danger">{w.lastError}</Callout> : null}
                  </div>
                  <div className="bidrl-finding__actions">
                    <Button
                      size="sm"
                      disabled={disabled || busy}
                      onClick={() =>
                        void act(
                          () => api.post(`/watchlists/${encodeURIComponent(w.id)}/run`),
                          `Queued a run of “${w.name}”.`,
                        )
                      }
                    >
                      Run now
                    </Button>
                    <Button
                      size="sm"
                      disabled={disabled || busy}
                      onClick={() =>
                        void act(
                          () => api.patch(`/watchlists/${encodeURIComponent(w.id)}`, { enabled: !w.enabled }),
                          w.enabled ? `Paused “${w.name}”.` : `Resumed “${w.name}”.`,
                        )
                      }
                    >
                      {w.enabled ? "Pause" : "Resume"}
                    </Button>
                    <Button
                      variant="danger"
                      size="sm"
                      disabled={disabled || busy}
                      onClick={() =>
                        void act(
                          () => api.del(`/watchlists/${encodeURIComponent(w.id)}`),
                          `Deleted “${w.name}” and its findings.`,
                        )
                      }
                    >
                      Delete
                    </Button>
                  </div>
                </div>
              ))
            : null}
        </Card>
      </Stack>
    </Page>
  );
}
