/** The pieces one lot is made of: its star, its thumb, its title, its note. */
import {
  createContext,
  useContext,
  useEffect,
  useState,
  type MouseEvent,
  type ReactNode,
} from "react";
import { Badge, Card, Dash, Hint, Input, Link, Stack, usePath } from "@cc/ui";
import { cents, comparableHint, locationLabelOrEmpty, type Lot } from "./model";
import { api } from "./api";
import { BidrlLink } from "./chrome";

/**
 * Announced when a star is toggled. Saving is a direct write with no event behind it, so
 * a screen whose whole list is defined by saving — Saved — has to be told, or the row it
 * no longer belongs on sits there until a reload.
 */
export const FavoriteChanged = createContext<() => void>(() => {});

/**
 * The lot drawer, when the screen is wrapped in its host. A lot link asks this to open the
 * lot over the current list instead of navigating away, so the filters, the scroll, and
 * the loaded pages behind it survive. It is `null` on screens without a host, where a lot
 * link is an ordinary navigation.
 */
export type LotDrawer = {
  id: string;
  open: (id: string) => void;
  close: () => void;
};

export const LotDrawerContext = createContext<LotDrawer | null>(null);

export function useLotDrawer(): LotDrawer | null {
  return useContext(LotDrawerContext);
}

/**
 * A link to one lot. It stays a real anchor, so a middle click or "open in new tab" still
 * lands on the lot's own page; a plain left click opens the drawer over the current screen
 * instead. On a lot page already the drawer would hide the page it duplicates, so there
 * it navigates as an ordinary link.
 */
export function LotLink({
  lot,
  className,
  ariaLabel,
  children,
}: {
  lot: Pick<Lot, "id" | "title">;
  className?: string | undefined;
  ariaLabel?: string | undefined;
  children?: ReactNode;
}) {
  const drawer = useLotDrawer();
  const onLotPage = usePath().startsWith("/bidrl/lot/");
  const onClick = (event: MouseEvent<HTMLAnchorElement>) => {
    if (!drawer || onLotPage) return;
    if (event.defaultPrevented || event.button !== 0) return;
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    drawer.open(lot.id);
  };
  return (
    <Link
      to={`/bidrl/lot/${encodeURIComponent(lot.id)}`}
      className={className}
      aria-label={ariaLabel}
      onClick={onClick}
    >
      {children ?? lot.title ?? lot.id}
    </Link>
  );
}

export function bucketTone(
  bucket: string,
): "neutral" | "ok" | "warn" | "danger" {
  if (bucket === "priced") return "ok";
  if (bucket === "worth_opening" || bucket === "research") return "warn";
  if (bucket === "discarded" || bucket === "rejected") return "danger";
  return "neutral";
}

/**
 * Saving a lot is a direct write, not a job, so the star has to answer immediately
 * rather than wait for the snapshot to come round again. It holds its own optimistic
 * state and falls back to the server's answer when the reload lands.
 */
export function FavoriteStar({ lot }: { lot: Lot }) {
  const [saved, setSaved] = useState(lot.favorite);
  const [busy, setBusy] = useState(false);
  const changed = useContext(FavoriteChanged);
  useEffect(() => setSaved(lot.favorite), [lot.favorite, lot.id]);

  const toggle = async () => {
    const next = !saved;
    setSaved(next);
    setBusy(true);
    try {
      const path = `/lots/${encodeURIComponent(lot.id)}/favorite`;
      if (next) await api.post(path);
      else await api.del(path);
      changed();
    } catch {
      setSaved(!next);
    } finally {
      setBusy(false);
    }
  };

  return (
    <button
      type="button"
      className={`bidrl-star${saved ? " bidrl-star--on" : ""}`}
      aria-pressed={saved}
      aria-label={
        saved ? `Unsave ${lot.title || lot.id}` : `Save ${lot.title || lot.id}`
      }
      title={saved ? "Saved — remove from Saved" : "Save for later"}
      disabled={busy}
      onClick={(e) => {
        e.preventDefault();
        e.stopPropagation();
        void toggle();
      }}
    >
      {saved ? "\u2605" : "\u2606"}
    </button>
  );
}

export function LotThumb({
  lot,
  className,
}: {
  lot: Lot;
  className?: string | undefined;
}) {
  if (!lot.thumbUrl) {
    /* A box, not a dash: a missing photo must not shorten the row it sits in. */
    return (
      <div
        className={
          className ? `${className}-empty` : "cc-lot-thumb bidrl-thumb-empty"
        }
      >
        <Dash />
      </div>
    );
  }
  return (
    <img
      className={className ?? "cc-lot-thumb"}
      src={lot.thumbUrl}
      alt=""
      width={className ? 220 : 48}
      height={className ? 220 : 48}
      loading="lazy"
      decoding="async"
    />
  );
}

export function LotThumbLink({
  lot,
  className,
}: {
  lot: Lot;
  className?: string | undefined;
}) {
  const thumb = <LotThumb lot={lot} className={className} />;
  if (!lot.thumbUrl) return thumb;
  return (
    <LotLink
      lot={lot}
      className={className ? `${className}-link` : undefined}
      ariaLabel={`Open ${lot.title || lot.id}`}
    >
      {thumb}
    </LotLink>
  );
}

/**
 * The saved note. Only on the lot page: a note is something you write once and read
 * later, so it does not need to be editable from every list that shows the lot.
 *
 * Saving a note also saves the lot, because writing "check the charger fits" about
 * something you have not kept is not a thing anyone means to do.
 */
export function LotNote({ lot }: { lot: Lot }) {
  const [draft, setDraft] = useState(lot.favoriteNote);
  const [saved, setSaved] = useState(lot.favoriteNote);
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    setDraft(lot.favoriteNote);
    setSaved(lot.favoriteNote);
  }, [lot.favoriteNote, lot.id]);

  const commit = async () => {
    const note = draft.trim();
    if (note === saved.trim()) return;
    setBusy(true);
    setFailed(false);
    try {
      await api.post(`/lots/${encodeURIComponent(lot.id)}/favorite`, { note });
      setSaved(note);
    } catch {
      setFailed(true);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title="Your note">
      <Stack>
        <Input
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onBlur={() => void commit()}
          onKeyDown={(e) => {
            if (e.key === "Enter") void commit();
          }}
          placeholder="Why you kept this — a measurement, a question, what to check"
          aria-label="Note"
          disabled={busy}
        />
        <Hint>
          {failed
            ? "Could not save that note."
            : lot.favorite
              ? "Saved with this lot. Writing a note keeps the lot too."
              : "Writing a note saves this lot to Saved."}
        </Hint>
      </Stack>
    </Card>
  );
}

/** The day you saved it. A time of day would be noise on a list you scan by date. */
export function savedOn(iso: string): string {
  if (!iso) return "";
  const at = new Date(iso);
  return Number.isNaN(at.getTime()) ? "" : at.toLocaleDateString();
}

export function LotMeta({ lot }: { lot: Lot }) {
  return (
    <>
      {lot.category ? <Badge>{lot.category}</Badge> : null}
      <Badge tone={bucketTone(lot.bucket)}>
        {lot.bucket.replace("_", " ")}
      </Badge>
    </>
  );
}

/**
 * Where the lot is. Shown on every card and row, and kept on narrow screens, because
 * ruling something out on the drive rather than on the price is the whole reason to
 * put it here — doing that from a phone is the common case, not the edge one.
 */
export function LotLocation({ lot }: { lot: Lot }) {
  const label = locationLabelOrEmpty(lot);
  if (!label) return null;
  return (
    <span className="bidrl-lot-location" title={`Auction location: ${label}`}>
      {label}
    </span>
  );
}

export function LotTitle({
  lot,
  showLotCode = true,
}: {
  lot: Lot;
  showLotCode?: boolean;
}) {
  const ident =
    lot.identification && lot.identification !== lot.title
      ? lot.identification
      : "";
  const hint = ident || (showLotCode ? lot.lotCode : "");
  return (
    <>
      <LotLink lot={lot}>{lot.title || lot.id}</LotLink>
      {hint || lot.url ? (
        <Hint>
          {hint}
          {lot.url ? (
            <>
              {hint ? " · " : ""}
              <BidrlLink href={lot.url} />
            </>
          ) : null}
        </Hint>
      ) : null}
    </>
  );
}

/** The compact identity block used by the triage table. It avoids repeating a long
 * external-link label in every row while keeping lot code, model, and location visible. */
export function LotTableTitle({ lot }: { lot: Lot }) {
  const ident =
    lot.identification && lot.identification !== lot.title
      ? lot.identification
      : "";
  return (
    <div className="bidrl-table-item">
      <LotThumbLink lot={lot} className="bidrl-table-thumb" />
      <div className="bidrl-table-item__body">
        <div className="bidrl-table-item__eyebrow">
          <span className="bidrl-table-item__lot-code">
            {lot.lotCode ? `Lot ${lot.lotCode}` : "Lot"}
          </span>
          {lot.category ? <Badge>{lot.category}</Badge> : null}
          <Badge tone={bucketTone(lot.bucket)}>
            {lot.bucket.replace("_", " ")}
          </Badge>
        </div>
        <div className="bidrl-table-item__title">
          <LotLink lot={lot}>{lot.title || lot.id}</LotLink>
          {lot.url ? (
            <BidrlLink
              href={lot.url}
              ariaLabel={`Open ${lot.title || lot.id} on BidRL`}
            >
              BidRL ↗
            </BidrlLink>
          ) : null}
        </div>
        <div className="bidrl-table-item__meta">
          {ident ? <span title={ident}>{ident}</span> : null}
          <LotLocation lot={lot} />
        </div>
      </div>
    </div>
  );
}

export function LotComparable({ lot }: { lot: Lot }) {
  if (lot.priceCents == null)
    return (
      <span className="bidrl-table-comp__value">
        <Dash />
      </span>
    );
  return (
    <>
      <span className="bidrl-table-comp__value">{cents(lot.priceCents)}</span>
      <Hint>
        {lot.sourceUrl ? (
          <a href={lot.sourceUrl} target="_blank" rel="noreferrer">
            {comparableHint(lot) || lot.sourceTitle || "source"}
          </a>
        ) : (
          comparableHint(lot) || <Dash />
        )}
      </Hint>
    </>
  );
}
