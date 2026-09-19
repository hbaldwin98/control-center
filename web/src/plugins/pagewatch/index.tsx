/**
 * Page Watch: a low-cost browser, AI, storage, event, and job canary.
 *
 * This file is the plugin's manifest and nothing else. The screen behind the route lives
 * in `screens/`, and the pieces it is built from sit beside it: `model` (the shapes it
 * reads), `api`, `data` (every read), and `checks` (a check, rendered).
 */
import type { PluginModule } from "@cc/ui";
import { Detail, Tile } from "./dashboard";
import { History } from "./screens/history";

const pagewatch: PluginModule = {
  id: "pagewatch",
  nav: [{ path: "/pagewatch", label: "Page Watch", icon: "eye" }],
  routes: [{ path: "/pagewatch", element: <History /> }],
  dashboard: {
    summary: "Checks one public page for expected text and content drift every six hours.",
    live: ["pagewatch.check.completed"],
    tile: Tile,
    detail: Detail,
  },
};

export default pagewatch;
