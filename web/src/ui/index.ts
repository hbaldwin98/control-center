/**
 * `@cc/ui` — the shared design system, transport, and live-data hooks.
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
export type { ApiErrorBody, BufferedSnapshot, PluginApi } from "./api";

export { idAbove, stream } from "./stream";
export type { StreamBuffer } from "./stream";

export { isValidPattern, matchesPattern } from "./pattern";

export { useEvents, useSnapshot, useStreamEpoch } from "./hooks";
export type {
  ApplyEvent,
  SnapshotState,
  UseEventsOptions,
  UseSnapshotOptions,
  UseSnapshotResult,
} from "./hooks";

export { formatDateTime, formatProgress, formatTime, formatUSD } from "./format";

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
  Loading,
  LogBlock,
  Money,
  Page,
  PageHeader,
  Row,
  Select,
  Stack,
  Table,
  Time,
  Toolbar,
} from "./components";
