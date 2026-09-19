/**
 * Navigation primitives.
 *
 * Plugins may not import the shell router, but they still have to navigate: without a
 * client-side link every in-plugin click is a full document load that tears down the
 * event stream, the snapshot cache, and the screen's own state. So the router is wrapped
 * here, in the one module plugins are allowed to import, and never exposed directly.
 *
 * This file is the only place in `@cc/ui` that knows react-router exists.
 */
import type { AnchorHTMLAttributes } from "react";
import {
  Link as RouterLink,
  useLocation,
  useNavigate as useRouterNavigate,
  useParams,
  useSearchParams,
} from "react-router-dom";

type LinkProps = Omit<AnchorHTMLAttributes<HTMLAnchorElement>, "href"> & { to: string };

/**
 * An in-application link. Same shape as an anchor, but it routes instead of reloading.
 * Use `<a href>` only for a genuinely external destination.
 */
export function Link({ to, children, ...rest }: LinkProps) {
  return (
    <RouterLink to={to} {...rest}>
      {children}
    </RouterLink>
  );
}

/** The current pathname. Unlike reading `window.location`, this re-renders on navigation. */
export function usePath(): string {
  return useLocation().pathname;
}

/**
 * The current query string, leading "?" included, or "" when there is none. Pairs with
 * `usePath` for a screen that has to remember the whole place it is at, not just which
 * one it is.
 */
export function useSearch(): string {
  return useLocation().search;
}

/**
 * Navigate from code, for the cases a link cannot express: after deleting the record the
 * current screen is showing, say. Prefer `Link` everywhere a person is choosing to go.
 *
 * A number is a history step, which is how a surface that pushed an entry to open (a
 * drawer) undoes exactly that entry when it closes.
 */
export function useNavigate(): (to: string | number) => void {
  const navigate = useRouterNavigate();
  return (to: string | number) => {
    if (typeof to === "number") navigate(to);
    else navigate(to);
  };
}

/** Path parameters for the matched route, so a screen never parses the URL itself. */
export function useRouteParams(): Record<string, string | undefined> {
  return useParams();
}

/** Whether a query write replaces the current history entry or pushes a new one. */
export type QueryHistory = "replace" | "push";

/**
 * One search parameter as state.
 *
 * Filters belong in the URL: it makes a filtered list shareable, and it means going into
 * a record and back returns the list the way it was left rather than reset. Writes
 * replace the current entry by default, so typing in a filter box does not bury the
 * previous screen under a stack of history.
 *
 * A surface that opens *over* the current screen (a drawer) instead passes
 * `{ history: "push" }` once, so the browser's back button closes it and returns to the
 * list exactly as it was. Later writes within that surface pass `"replace"` again.
 */
export function useQueryState(
  key: string,
  fallback = "",
  options?: { history?: QueryHistory },
): [string, (next: string, setOptions?: { history?: QueryHistory }) => void] {
  const [params, setParams] = useSearchParams();
  const value = params.get(key) ?? fallback;
  const defaultHistory = options?.history ?? "replace";
  const set = (next: string, setOptions?: { history?: QueryHistory }) => {
    setParams(
      (prev) => {
        const copy = new URLSearchParams(prev);
        if (next === "" || next === fallback) copy.delete(key);
        else copy.set(key, next);
        return copy;
      },
      { replace: (setOptions?.history ?? defaultHistory) === "replace" },
    );
  };
  return [value, set];
}
