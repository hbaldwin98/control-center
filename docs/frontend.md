# Frontend

A single React/TypeScript SPA. The shell provides routing, layout, navigation, auth, the
live data connection, and a shared component library. Plugins provide routes and
components.

---

## Layout

```
web/src/
  shell/            layout, nav, auth, router, SSE client
  ui/               shared design system — the ONLY thing plugins import
  core/             dashboard, jobs, events, costs, settings, plugin admin
  plugins/
    hello/index.tsx
    bidrl/index.tsx
  plugins.ts        ← the only file importing plugin modules
```

This mirrors the backend deliberately: one registration file, plugins isolated behind a
declared entry point. `web/src/plugins/<id>` is the canonical source of plugin UI metadata
and code; backend manifests contain no routes, navigation, icons, or frontend entry paths.

---

## The plugin UI contract

One entry point per plugin, importing only `@cc/ui`:

```tsx
import type { PluginModule } from "@cc/ui";

const bidrl: PluginModule = {
  id: "bidrl",
  nav: [{ path: "/bidrl", label: "BIDRL", icon: "gavel" }],
  routes: [
    { path: "/bidrl", element: <Feed /> },
    { path: "/bidrl/auction/:id", element: <Auction /> },
  ],
  dashboard: {
    summary: "Watches saved searches and scores what it finds.",
    live: ["bidrl.**"],
    tile: Tile,      // compact, on the dashboard grid
    detail: Panel,   // full, on /plugins/bidrl
  },
};

export default bidrl;
```

Registered in one place:

```ts
// web/src/plugins.ts
import bidrl from "./plugins/bidrl";
import hello from "./plugins/hello";

export const plugins = [hello, bidrl];
```

The build rejects duplicate frontend IDs and route or navigation collisions. Plugin route
and navigation paths must stay below `/<plugin-id>`.
At startup, before rendering plugin UI, the shell compares every `PluginModule.id` with
the authenticated backend descriptors and fails closed on unknown, missing, or duplicate
IDs. The frontend module owns its navigation, routes, and icons.

### Dashboard contributions

`dashboard` is optional and every field in it is optional. A plugin that contributes
nothing still gets a dashboard tile, built from what the host knows: state, spend against
budget, open and failed jobs, and the reason it is disabled. What the plugin adds is its
own reading of its own data.

`live` is the plugin declaring that its surfaces update themselves from the stream. The
shell takes it at its word and shows a live indicator, the time of the last matching event,
and a ten-minute activity history on the tile. It is a claim about behaviour, so the build
rejects a pattern that could never match — a permanently, silently wrong indicator is worse
than none. A tile only reads "live" when the plugin declared it **and** the one shared
connection is actually up; while the browser is reconnecting, every tile says so.

`tile` and `detail` are ordinary components, given `{ pluginId, enabled }` and rendered
behind an error boundary. `enabled` is there so a surface can rest instead of firing
requests the host will reject. A surface that throws is replaced by a note naming the
plugin: the dashboard is the screen an operator reaches for the kill switch from, so one
broken tile must never take the page — or the switch — with it.

### The rule that keeps this modular

> Plugin components may import from `@cc/ui` and their own directory. **Never** from
> `shell/` or `core/`.

For v1 these are compiled into the main bundle. The narrow import surface keeps dynamic or
out-of-process plugins as a compatible direction, but that move still needs loading
protocols, adapters, version negotiation, and isolation. It is not merely a build or
transport change.

### Plugin HTTP client

`@cc/ui` exports a plugin-scoped client so components never assemble host API URLs or
security headers themselves:

```tsx
import { pluginApi } from "@cc/ui";

const api = pluginApi("bidrl");
const { data: auction } = await api.snapshot<Auction>(`/auctions/${id}`, {
  events: "bidrl.**",
});
await api.post(`/scans`, { auctionId: id });
```

The helper prefixes `/api/plugins/<id>`, sends same-origin credentials, adds the current
session-bound CSRF token to mutations, and parses the standard JSON error envelope. It
turns error code `plugin_disabled` into a typed `PluginDisabledError`; an unrelated `503`
remains a service error. Plugin components use REST for initial snapshots and mutations,
so they can load data and start work such as scans without importing shell internals.
`snapshot<T>` returns `Snapshot<T>` and coordinates its event boundary with the shared
stream; plain `get<T>` is for non-state responses that need no live handoff.

---

## Live data

One authenticated SSE stream at `/api/stream` sends every authorized committed event. The
shell opens it once and multiplexes dot-segment patterns locally; plugins do not create
connections or send server-side subscription patterns.

```tsx
const deals = useEvents("bidrl.deal_found");
const cost  = useEvents<AIUsage>("core.ai.usage", {
  filter: e => e.payload.plugin === "bidrl",
});
```

The wire and hook envelope is:

```ts
type Event<T = unknown> = {
  id: string; // decimal int64; compare as BigInt, never Number
  type: string;
  source: string;
  subject: string;
  payload: T;
  createdAt: string;
};

type Snapshot<T> = {
  data: T;
  asOfEventId: string;
};
```

Everything live in the UI rides this one connection — job progress/log invalidations,
cost tickers, plugin state changes, and notification invalidations. REST provides initial
snapshots and mutations; SSE applies later changes. There is no second realtime mechanism
and no polling.

`useEvents` is provided by `@cc/ui`, so a plugin gets live data without knowing how the
transport works. Two more hooks ride the same connection and open nothing of their own:

```tsx
const status   = useStreamStatus();          // idle | connecting | live | reconnecting | offline | reset
const activity = useActivity("bidrl.**");    // { total, lastAt, last, buckets }
```

`useStreamStatus` reflects the actual `EventSource`, so nothing in the UI can claim to be
live while the browser is retrying. `useActivity` keeps arrival timestamps only — never
payloads — which is what lets a tile show that a plugin is working without the shell
knowing what any of its events mean.

The shell first loads `/api/bootstrap`, a `Snapshot` containing the initial core state,
then opens the one shared stream after that boundary. SSE tails `core_events` directly
rather than a lossy live-subscription queue. Each message
has the persisted event ID. On a slow-client queue overflow the server closes the stream;
the reconnect replays from the last delivered ID instead of silently dropping an event.
The browser sends standard `Last-Event-ID` on automatic reconnect, and `/api/stream`
accepts `?after=<id>` for a newly constructed client. The shell suppresses duplicate IDs.
Heartbeats keep intermediaries and stale-connection detection working. If the requested
ID predates retention, the server emits a reset signal with the oldest retained ID.

Every state snapshot response is `{ data, asOfEventId }`, with decimal-string IDs. The
server reads the data and current event-log tail from one SQLite read transaction. For a
snapshot loaded after bootstrap, `api.snapshot` starts a pattern-filtered local buffer on
the already-open shared stream before sending the REST request, installs the snapshot,
then applies buffered events strictly above `asOfEventId`. It never opens another stream.
If reset occurs, the shell pauses delivery, reloads bootstrap and each mounted snapshot
through its registered loader, and opens one stream after the new bootstrap boundary.
Job progress/log and notification lifecycle events are invalidations; clients refetch the
affected resource rather than reconstructing it from partial event payloads.

---

## Core screens

| Screen | Shows |
|---|---|
| Dashboard | one live tile per plugin in a grid, plus totals, running jobs, and recent alerts |
| Plugin detail | one plugin in full: live activity, its own surface, jobs, spend, and every control |
| Plugins | enable/disable toggles, budgets, health, last error, schema-backed config |
| Jobs | queue, history, per-job progress and logs, cancel; filterable by plugin from the URL |
| Events | live event log, filterable by pattern, shortcuts for plugin/job/AI/browser/alerts |
| Costs | spend by plugin → job → logical model, over time |
| Settings | credentials (with re-auth), read-only effective model routes |

The dashboard is a grid of plugin tiles. Each tile carries the plugin's state, what it has
spent today against its daily budget, its open and failed work, its live indicator and
activity history, and whatever surface the plugin contributed. Clicking one opens
`/plugins/<id>`: the same live view at full size, that plugin's jobs and event feed, its
spend in every budget window, and the same controls the Plugins screen offers — kill
switch, budgets, and config — because an operator who has drilled into a plugin should not
have to navigate back to turn it off.

The Plugins screen is where the host-capability kill switch lives. Disabled plugins are
greyed with the reason and timestamp, never hidden. Disable rejects new host-managed jobs,
AI dispatches, event handlers, plugin HTTP requests, event publications, and storage/blob
mutations, closes that plugin's browser sessions, and cancels admitted contexts. Reads and
logs remain available. It does not claim to terminate trusted in-process code that ignores
cancellation or uses direct networking. An already-admitted paid call may finish and
remains accounted. Schema-backed plugin config is edited on the same screen.

For `Automated: true` plugins, the toggle is disabled until a daily budget is set, with the
reason shown inline — the UI half of the guardrail enforced in
[`policy`](modules/policy.md).

---

## Auth

There is one administrator principal, with no user-management or password-reset flow.
First-run bootstrap is accepted only on loopback and requires a one-time token to set the
admin password. Non-loopback access requires TLS; the password hash is Argon2id.

The server uses a `__Host-` `HttpOnly`, `Secure`, `SameSite=Lax` session cookie. Sessions
use `Path=/`, omit `Domain`, have 12-hour absolute and idle policies, rotate on
authentication, and are invalidated on logout. Every mutation requires a synchronizer
CSRF token bound to that session plus an allowed `Origin`. Credential changes require
password reauthentication within five minutes.

Credentials administration is a lower-layer interface consumed by the web API. List and
status responses never contain secret material. Settings supports API-key create/replace
and OAuth with server-generated state, PKCE S256, and a callback bound to the initiating
session; it never reads an existing key back. Notification channel secrets are credential
entries referenced by channel configuration.
