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
| Models | AI providers, the model catalogs they publish, and the routes plugins name |
| Settings | credentials (with re-auth), API keys, and OAuth logins |

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

The Models screen is administration, not secrets: it edits providers and routes with a
session and CSRF, and shows credential ids without ever showing credential material. A
model catalog is read from its last fetch until someone asks to refresh it, so opening the
screen never calls out to every configured provider. A route's prices are the ones it was
saved with, and the editor states the reservation a call will take before it is saved.

For `Automated: true` plugins, the toggle is disabled until a daily budget is set, with the
reason shown inline — the UI half of the guardrail enforced in
[`policy`](modules/policy.md).

---

## Design system

`@cc/ui` owns every visual decision. A screen composes its exports; it does not write
inline styles, hand-roll a `<table>`, or borrow a class from another component. The core
screens and the `hello` plugin are the worked examples.

**Layout** — `Page`, `PageHeader`, `Card`, `Stack`, `Grid`, `Row`, `Toolbar`.
`Toolbar` is the filter bar above a listing; its controls wrap rather than overflow.

**Data** — `Table` renders the header row and its own horizontal scroll container, so a
wide table scrolls inside its card and the page never scrolls sideways. `ActionsHeader`
is the header cell for a column of buttons. `Time`, `Money`, and `Dash` render the three
values that appear on every screen; `Time` shows local time and puts the exact instant in
the tooltip.

**States** — `Async` renders a snapshot's three outcomes so no two screens disagree about
what "loading" looks like:

```tsx
<Async state={jobs} loading="Loading jobs…" empty="No jobs yet.">
  {(list) => <Table head={…}>{list.map(row)}</Table>}
</Async>
```

It falls back to `Loading` and a danger `Callout` on its own. Use `Loading`, `EmptyState`,
and `Callout` directly only when a screen merges several sources, as `Events` does.

**Text** — `Hint` is muted secondary text and `LogBlock` is a monospaced log or error
body. `cc-field__hint` belongs to `Field` and is not a general muted-text class.

**Formatters** — `formatUSD`, `formatDateTime`, `formatTime`, and `formatProgress` are
exported for the cases that need a string rather than an element. Money is micro-USD
everywhere; nothing divides by 1,000,000 in a screen.

Tokens live in `ui/styles.css`: one spacing scale, one focus ring, and a palette declared
once and applied to both the system preference and an explicit `data-theme`. The shell
collapses its sidebar to a top strip below 720px.

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
