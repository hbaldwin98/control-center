/**
 * The contract between the shell and plugin UI.
 *
 * Plugin components may import from `@cc/ui` and their own directory. Never from
 * `shell/` or `core/`.
 */
import type { ComponentType, ReactElement } from "react";

export type NavIconName =
  | "command"
  | "gavel"
  | "jobs"
  | "sessions"
  | "inbox"
  | "plugins"
  | "events"
  | "costs"
  | "models"
  | "settings"
  | "plugin"
  | "sparkle"
  | "eye"
  | "bolt"
  | "bell"
  | "search";

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
  /** A shared, frontend-only icon name. Plugins never provide SVG or shell components. */
  icon?: NavIconName;
  /** Render this plugin entry in the shell's primary command navigation. */
  topLevel?: boolean;
};

export type RouteDef = {
  /** Must stay below `/<plugin-id>`. */
  path: string;
  element: ReactElement;
};

/**
 * What the shell hands a plugin's dashboard surface.
 *
 * A surface is rendered whether or not the plugin is enabled; `enabled` is there so it
 * can degrade to a resting state instead of firing requests the host will reject.
 */
export type PluginSurfaceProps = {
  pluginId: string;
  enabled: boolean;
};

/**
 * A plugin's optional contribution to the dashboard.
 *
 * Declaring `live` is the plugin saying "my surfaces update themselves from the stream".
 * The shell takes it at its word: it shows a live indicator, the time of the last matching
 * event, and a short activity history on the plugin's tile. A plugin that declares nothing
 * still gets a tile — built from what the host knows about it — just not a live one.
 */
export type PluginDashboard = {
  /** One line under the plugin's name, on the tile and on its detail screen. */
  summary?: string;
  /** Compact body rendered inside the plugin's dashboard tile. Keep it to a few lines. */
  tile?: ComponentType<PluginSurfaceProps>;
  /** Full panel rendered on the plugin's detail screen, above the host's own sections. */
  detail?: ComponentType<PluginSurfaceProps>;
  /** Event patterns these surfaces are live on. Presence turns the live indicator on. */
  live?: string[];
};

/** One entry point per plugin. The frontend module owns its nav, routes, and icons. */
export type PluginModule = {
  id: string;
  nav: NavItem[];
  routes: RouteDef[];
  /**
   * The path of the plugin's home screen. Defaults to `nav[0]?.path`.
   *
   * Ordering nav for the tab strip should not decide where the plugin opens from:
   * set `entry` when the home screen is not the first nav item. It must name one of
   * the plugin's own paths (registration rejects anything else).
   */
  entry?: string;
  /** Optional. Without it the plugin still appears on the dashboard, statically. */
  dashboard?: PluginDashboard;
};

/** The backend's authenticated view of a registered plugin. */
export type PluginDescriptor = {
  id: string;
  name: string;
  enabled: boolean;
};
