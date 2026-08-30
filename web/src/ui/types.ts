/**
 * The contract between the shell and plugin UI.
 *
 * Plugin components may import from `@cc/ui` and their own directory. Never from
 * `shell/` or `core/`.
 */
import type { ReactElement } from "react";

/** A committed event as it appears on the wire and in `useEvents`. */
export type Event<T = unknown> = {
  /** Decimal int64. Compare as BigInt, never as Number. */
  id: string;
  type: string;
  source: string;
  subject: string;
  payload: T;
  createdAt: string;
};

/**
 * Every state snapshot response. `asOfEventId` is the boundary above which buffered
 * stream events are applied.
 */
export type Snapshot<T> = {
  data: T;
  asOfEventId: string;
};

export type NavItem = {
  /** Must stay below `/<plugin-id>`. */
  path: string;
  label: string;
  icon?: string;
};

export type RouteDef = {
  /** Must stay below `/<plugin-id>`. */
  path: string;
  element: ReactElement;
};

/** One entry point per plugin. The frontend module owns its nav, routes, and icons. */
export type PluginModule = {
  id: string;
  nav: NavItem[];
  routes: RouteDef[];
};

/** The backend's authenticated view of a registered plugin. */
export type PluginDescriptor = {
  id: string;
  name: string;
  enabled: boolean;
};
