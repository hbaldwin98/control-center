/**
 * `@cc/ui` — the shared design system, transport, and live-data hooks.
 *
 * This is the ONLY module plugin UI may import, besides its own directory. It deliberately
 * imports nothing from `shell/` or `core/`, so the dependency arrow never reverses.
 */
export type {
  Event,
  NavItem,
  PluginDashboard,
  PluginDescriptor,
  PluginModule,
  PluginSurfaceProps,
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
export type { ApiErrorBody, BufferedSnapshot, PluginApi } from "./api";

export { idAbove, stream } from "./stream";
export type { StreamBuffer, StreamStatus } from "./stream";

export { isValidPattern, matchesPattern } from "./pattern";

export { useActivity, useEvents, useNow, useSnapshot, useStreamEpoch, useStreamStatus } from "./hooks";
export type {
  Activity,
  ApplyEvent,
  SnapshotState,
  UseActivityOptions,
  UseEventsOptions,
  UseSnapshotOptions,
  UseSnapshotResult,
} from "./hooks";

export {
  Badge,
  Button,
  Callout,
  Card,
  EmptyState,
  Field,
  Grid,
  Input,
  LiveDot,
  Meter,
  Metric,
  Page,
  PageHeader,
  RelativeTime,
  Row,
  Sparkline,
  Stack,
} from "./components";

export { formatRelative, formatTime, formatUSD, formatWhen } from "./format";
