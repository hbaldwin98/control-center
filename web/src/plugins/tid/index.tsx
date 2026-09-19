/**
 * TID — energy usage from Turlock Irrigation District.
 *
 * Syncs daily kWh and billing history from My TID, then shows month-to-date metrics,
 * peak/off-peak use, demand, and a model-written insight.
 *
 * This file is the plugin's manifest and nothing else. The screen behind the route lives
 * in `screens/`, and the pieces it is built from sit beside it: `model` (the shapes it
 * reads and how it formats a number), `api`, `data` (every read), `metrics`, `chart`,
 * and `billing`.
 */
import type { PluginModule } from "@cc/ui";
import { Detail, Tile } from "./dashboard";
import { Usage } from "./screens/usage";
import "./index.css";

const tid: PluginModule = {
  id: "tid",
  nav: [{ path: "/tid", label: "TID", icon: "bolt" }],
  routes: [{ path: "/tid", element: <Usage /> }],
  dashboard: {
    summary: "Daily kWh from My TID, with a short reading of the last month.",
    live: ["tid.synced", "tid.insight"],
    tile: Tile,
    detail: Detail,
  },
};

export default tid;
