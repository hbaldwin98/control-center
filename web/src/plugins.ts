/**
 * The only file in the frontend that imports plugin modules.
 *
 * `web/src/plugins/<id>` is the canonical source of plugin UI metadata and code; backend
 * manifests contain no routes, navigation, icons, or frontend entry paths.
 */
import type { PluginModule } from "@cc/ui";
import hello from "./plugins/hello";
import tid from "./plugins/tid";

export const plugins: PluginModule[] = [hello, tid];
