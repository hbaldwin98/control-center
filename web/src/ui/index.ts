/**
 * `@cc/ui` — the shared design system and transport helpers.
 *
 * This is the ONLY module plugin UI may import, besides its own directory. It deliberately
 * imports nothing from `shell/` or `core/`, so the dependency arrow never reverses.
 */
export type {
  Event,
  NavItem,
  PluginDescriptor,
  PluginModule,
  RouteDef,
  Snapshot,
} from "./types";

export {
  ApiError,
  PluginDisabledError,
  api,
  getCsrfToken,
  pluginApi,
  setCsrfToken,
} from "./api";
export type { ApiErrorBody, PluginApi } from "./api";

export {
  Badge,
  Button,
  Callout,
  Card,
  EmptyState,
  Field,
  Grid,
  Input,
  Page,
  PageHeader,
  Row,
  Stack,
} from "./components";
