/**
 * BIDRL — score auction lots from photographs, not titles.
 *
 * Imports `@cc/ui` and this directory only. Auction and lot ids come from the path so
 * this module never imports the shell router. One sidebar item; Feed / Auctions / Lots
 * are in-plugin tabs.
 *
 * This file is the plugin's manifest and nothing else. The screens behind these routes
 * live in `screens/`, the pieces they are built from in the modules beside this one:
 * `data` (every read), `live` (the bid feed), `lots` (a lot, rendered), `chrome`
 * (navigation and layout), `actions` (queueing a job), `sorting`, `place`, and `api`.
 */
import type { PluginModule } from "@cc/ui";
import { Detail, Tile } from "./dashboard";
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

const bidrl: PluginModule = {
  id: "bidrl",
  nav: [{ path: "/bidrl", label: "BIDRL" }],
  routes: [
    { path: "/bidrl", element: <Overview /> },
    { path: "/bidrl/auctions", element: <Auctions /> },
    { path: "/bidrl/lots", element: <LotsCatalog /> },
    { path: "/bidrl/findings", element: <Findings /> },
    { path: "/bidrl/watchlists", element: <Watchlists /> },
    { path: "/bidrl/saved", element: <SavedLots /> },
    { path: "/bidrl/automation", element: <AutomationScreen /> },
    { path: "/bidrl/intent", element: <IntentSearch /> },
    { path: "/bidrl/auction/:id", element: <AuctionView /> },
    { path: "/bidrl/lot/:id", element: <LotView /> },
  ],
  dashboard: {
    summary: "Scores lots from photographs. Collect a SITES auction, then scan.",
    live: ["bidrl.**"],
    tile: Tile,
    detail: Detail,
  },
};

export default bidrl;
