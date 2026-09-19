/**
 * BIDRL — score auction lots from photographs, not titles.
 *
 * Imports `@cc/ui` and this directory only. Auction and lot ids come from the path so
 * this module never imports the shell router. The root Auctions entry is promoted into
 * the shell; the rest of the workflow stays in the plugin's tab strip.
 *
 * This file is the plugin's manifest and nothing else. The screens behind these routes
 * live in `screens/`, the pieces they are built from in the modules beside this one:
 * `data` (every read), `live` (the bid feed), `lots` (a lot, rendered), `chrome`
 * (navigation and layout), `actions` (queueing a job), `sorting`, `place`, and `api`.
 */
import type { ReactNode } from "react";
import type { PluginModule } from "@cc/ui";
import { Detail, Tile } from "./dashboard";
import { LotDrawerHost } from "./drawer";
import { AuctionView } from "./screens/auction";
import { Auctions } from "./screens/auctions";
import { AutomationScreen } from "./screens/automation";
import { LotsCatalog } from "./screens/catalog";
import { Findings } from "./screens/findings";
import { IntentSearch } from "./screens/intent";
import { LotView } from "./screens/lot";
import { Overview } from "./screens/overview";
import { SavedLots } from "./screens/saved";
import { Watchlists } from "./screens/watchlists";
import "./index.css";

// Every screen gets the drawer host, so any lot link anywhere in the workflow can open
// the lot over the current screen instead of leaving it. The screen element is created
// once, so changing `?lot=` never remounts the list underneath.
const drawer = (screen: ReactNode) => <LotDrawerHost>{screen}</LotDrawerHost>;

const bidrl: PluginModule = {
  id: "bidrl",
  nav: [{ path: "/bidrl", label: "Auctions", icon: "gavel", topLevel: true }],
  routes: [
    { path: "/bidrl", element: drawer(<Overview />) },
    { path: "/bidrl/auctions", element: drawer(<Auctions />) },
    { path: "/bidrl/lots", element: drawer(<LotsCatalog />) },
    { path: "/bidrl/findings", element: drawer(<Findings />) },
    { path: "/bidrl/watchlists", element: drawer(<Watchlists />) },
    { path: "/bidrl/saved", element: drawer(<SavedLots />) },
    { path: "/bidrl/automation", element: drawer(<AutomationScreen />) },
    { path: "/bidrl/intent", element: drawer(<IntentSearch />) },
    { path: "/bidrl/auction/:id", element: drawer(<AuctionView />) },
    { path: "/bidrl/lot/:id", element: drawer(<LotView />) },
  ],
  dashboard: {
    summary: "Scores lots from photographs. Collect a SITES auction, then scan.",
    live: ["bidrl.**"],
    tile: Tile,
    detail: Detail,
  },
};

export default bidrl;
