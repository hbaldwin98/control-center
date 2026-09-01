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

export { Link, useNavigate, usePath, useQueryState, useRouteParams } from "./nav";

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

export { formatDateTime, formatProgress, formatRelative, formatRemaining, formatTime, formatUSD, parseInstant } from "./format";

export {
  ActionsHeader,
  Async,
  Badge,
  Button,
  Callout,
  Card,
  Checkbox,
  Dash,
  EmptyState,
  Field,
  Grid,
  Hint,
  Input,
  LiveDot,
  Loading,
  LogBlock,
  Meter,
  Metric,
  Money,
  Page,
  PageHeader,
  RelativeTime,
  Countdown,
  Row,
  Select,
  Sparkline,
  Stack,
  SortHeader,
  Table,
  Tabs,
  Textarea,
  Time,
  Toolbar,
} from "./components";
export { PluginAIHint } from "./setup";
