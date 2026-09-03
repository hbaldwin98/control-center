/** Where you were on each list screen. */
import {
  useEffect,
  useRef,
} from "react";
import {
  useSearch,
} from "@cc/ui";


/**
 * Where you were on each list screen.
 *
 * Filters live in the query string, so the browser's own back button already restores
 * them. What it cannot restore is a tab click or a crumb: those are fresh navigations to
 * a bare path, and they used to land on a reset list at the top. So every list screen
 * records the query string it is showing and how far down it is scrolled, and the links
 * that lead back to it carry that query string.
 *
 * sessionStorage rather than a module variable: a reload of the shell is still the same
 * visit, and it costs nothing to survive one.
 */
type Place = { search: string; scroll: number };

const PLACES_KEY = "bidrl.places";

function readPlaces(): Record<string, Place> {
  try {
    const raw = sessionStorage.getItem(PLACES_KEY);
    const parsed: unknown = raw ? JSON.parse(raw) : null;
    if (!parsed || typeof parsed !== "object") return {};
    return parsed as Record<string, Place>;
  } catch {
    return {};
  }
}

function writePlace(path: string, patch: Partial<Place>) {
  const all = readPlaces();
  all[path] = { search: "", scroll: 0, ...all[path], ...patch };
  try {
    sessionStorage.setItem(PLACES_KEY, JSON.stringify(all));
  } catch {
    // Storage refused (private mode, quota). Losing the place is not worth an error.
  }
}

/** A path with the filters it was last left with, for a link that means "back to that list". */
export function remembered(path: string): string {
  return `${path}${readPlaces()[path]?.search ?? ""}`;
}

/** The shell's scrolling element. Screens scroll inside it, not on the document. */
function scroller(): HTMLElement | null {
  const el = document.querySelector(".cc-main");
  return el instanceof HTMLElement ? el : null;
}

/**
 * Records this screen's filters and scroll offset, and restores the offset once there is
 * something to scroll. `ready` is what says the rows are on the page: restoring before
 * the list renders would scroll a short document and land at the top.
 */
export function usePlace(path: string, ready: boolean) {
  const search = useSearch();

  useEffect(() => {
    writePlace(path, { search });
  }, [path, search]);

  const restored = useRef(false);
  useEffect(() => {
    if (!ready || restored.current) return;
    restored.current = true;
    const top = readPlaces()[path]?.scroll ?? 0;
    if (top <= 0) return;
    // After paint, so the list has its full height and the offset is reachable.
    const frame = requestAnimationFrame(() => scroller()?.scrollTo({ top }));
    return () => cancelAnimationFrame(frame);
  }, [path, ready]);

  useEffect(() => {
    const el = scroller();
    if (!el) return;
    // Coalesced to one write per frame: scrolling fires far faster than storage wants.
    let frame = 0;
    const onScroll = () => {
      if (frame) return;
      frame = requestAnimationFrame(() => {
        frame = 0;
        writePlace(path, { scroll: el.scrollTop });
      });
    };
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => {
      if (frame) cancelAnimationFrame(frame);
      el.removeEventListener("scroll", onScroll);
      writePlace(path, { scroll: el.scrollTop });
    };
  }, [path]);
}
