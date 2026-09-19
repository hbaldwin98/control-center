/**
 * Hello — the validating plugin's UI.
 *
 * It exercises the whole frontend contract: a full screen, a live dashboard tile, and a
 * detail panel, all from one `useTicks` hook that loads a snapshot and folds later events
 * into it. Imports `@cc/ui` and this directory only.
 *
 * This file is the plugin's manifest and nothing else — the layout every plugin here
 * follows: `model` (the shapes it reads), `api`, `data` (every read), `ticks` (what the
 * screen and the tile share), `screens/`, and `dashboard`.
 */
import type { PluginModule } from "@cc/ui";
import { Detail, Tile } from "./dashboard";
import { History } from "./screens/history";

const hello: PluginModule = {
  id: "hello",
  nav: [{ path: "/hello", label: "Hello", icon: "hello" }],
  routes: [{ path: "/hello", element: <History /> }],
  dashboard: {
    summary: "Ticks a row, a blob, an event, and a tiny AI call every minute.",
    live: ["hello.ticked"],
    tile: Tile,
    detail: Detail,
  },
};

export default hello;
