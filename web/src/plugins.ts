/**
 * The only file in the frontend that imports plugin modules.
 *
 * `web/src/plugins/<id>` is the canonical source of plugin UI metadata and code; backend
 * manifests contain no routes, navigation, icons, or frontend entry paths.
 *
 * Plugins arrive at milestone 7 (`hello`) and 9 (`bidrl`).
 */
import type { PluginModule } from "@cc/ui";

export const plugins: PluginModule[] = [];
