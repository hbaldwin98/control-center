/** The plugin's own API client. Every request in this plugin goes through it. */
import { pluginApi } from "@cc/ui";

export const api = pluginApi("tid");
