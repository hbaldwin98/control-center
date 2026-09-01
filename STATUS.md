# Implementation status

Tracks [`DESIGN.md`](DESIGN.md) §8 build order. Updated with every milestone commit.

Legend: ✅ complete · 🔨 in progress · ⬜ todo

| # | Milestone | State | Done when |
|---|---|---|---|
| 1 | Skeleton | ✅ | `cmd`, config, SQLite, migrations, TLS-aware HTTP server, first-run bootstrap, session auth, CSRF, React shell boot. |
| 2 | `storage` + `events` | ✅ | Transactional event insertion works; durable delivery is serial and at-least-once; SSE replay and reset work. |
| 3 | `policy` | ✅ | Disable admission/cancellation works; persisted integer micro-USD reservations settle and release atomically. |
| 4 | `jobs` | ✅ | Enqueue, retries, cancellation, progress, and job UI work; disabled plugins cannot admit jobs. |
| 5 | `credentials` + `ai` | ✅ | Key replacement and OAuth work without exposing secrets; one provider records usage and settles reservations. |
| 6 | `pluginhost` + `host` | ✅ | Registry, facade, lifecycle, enforcement matrix all pass. |
| 7 | `hello` | ✅ | The validating plugin passes its acceptance test. |
| 8 | `browser` | ✅ | Host-managed sessions, allowlist, fake backend, and kill-switch close all pass. |
| 9 | `notifications` | ✅ | Channels use credential entries; rules and defaults deliver committed events. |
| 10 | `tid` | ✅ | Daily Turlock Irrigation District usage, metrics, and insight flow through host capabilities. |
| 11 | `pagewatch` | ✅ | A cheap browser-to-AI confidence plugin produces history, cost data, and actionable alerts. |
| 12 | `bidrl` | ✅ | The first real plugin. |
| 13 | Future | ⬜ | Harness sessions, terminal visibility, external gateway, out-of-process plugins. |

---

## Milestone 1 — Skeleton ✅

| Feature | State | Notes |
|---|---|---|
| `storage`: serialized writer + bounded reader pool, WAL, finite busy timeout | ✅ | `internal/core/storage/sqlite.go` |
| `storage`: `DB.Tx` commit/rollback on error, cancellation, panic | ✅ | |
| `storage`: nested transaction fails fast with `ErrNestedTx` | ✅ | Would otherwise deadlock on the writer. |
| `storage`: per-namespace migrations with name+SQL checksums | ✅ | Changed, removed, or inserted-below-high-water-mark migrations fail startup. |
| `config`: loopback may serve HTTP, everything else requires TLS | ✅ | Enforced in `Validate`. No secrets in config, by construction. |
| `web`: first-run bootstrap, one-time token, loopback-only | ✅ | Closed once an administrator exists. |
| `web`: Argon2id password hashing | ✅ | Parameters stored in the encoded hash. |
| `web`: `__Host-` session cookie, 12h absolute + idle, rotation, logout | ✅ | |
| `web`: CSRF synchronizer token + `Origin` check on mutations | ✅ | |
| `web`: password reauthentication window | ✅ | Consumed by credentials administration at milestone 5. |
| `web`: `{data, asOfEventId}` snapshot envelope | ✅ | |
| frontend: shell (auth gate, layout, router), `@cc/ui` | ✅ | Plugin UI may import `@cc/ui` and its own directory only. |
| frontend: plugin reconciliation fails closed | ✅ | Unknown, missing, or duplicate ids; paths outside `/<plugin-id>`. |

## Milestone 2 — `storage` + `events` ✅

| Feature | State | Notes |
|---|---|---|
| `storage`: filesystem blobs with authoritative SQLite metadata | ✅ | Stage → fsync → rename → atomic metadata swap. |
| `storage`: blob key grammar validated before filesystem access | ✅ | |
| `storage`: per-object limit and per-scope quota, delta-charged | ✅ | A failed write leaves the existing blob untouched. |
| `storage`: startup recovery of staged and orphaned files | ✅ | A crash exposes the old blob or the new one, never a partial. |
| `storage`: `WithMutationAdmission` hook on `Put`/`Delete` | ✅ | The point `pluginhost` checks policy at, in the metadata transaction. |
| `events`: `Publish` / `PublishTx`, type/source/payload validation | ✅ | A rollback removes the event. |
| `events`: pattern matching (`*`, `**`) and `Query` | ✅ | |
| `events`: live subscriptions (bounded, lossy, by design) | ✅ | |
| `events`: durable subscriptions, serial and at-least-once | ✅ | Nonmatching rows advance the cursor immediately. |
| `events`: `SubscribeDurableTx` commits handler writes with the cursor | ✅ | |
| `events`: retry policy, pause on poison event, `subscription_paused` | ✅ | Later events do not pass a paused cursor. |
| `events`: database-backed dispatch lease, one dispatcher per name | ✅ | Verified across two `Log` instances. |
| `events`: retention honouring age and cursor pins, disk-pressure report | ✅ | |
| `events`: cursor administration (retry, skip, reset, remove) | ✅ | |
| `web`: `/api/stream` SSE tailing the persisted log | ✅ | `Last-Event-ID`, `?after=`, heartbeats, write-deadline close. |
| `web`: reset signal when a resume point predates retention | ✅ | |
| `web`: `/api/events` query and subscriber administration | ✅ | |
| `web`: authenticated blob route with `nosniff` + attachment default | ✅ | Inline only for an explicit safe-image allowlist. |
| frontend: one shared stream, local pattern multiplexing, `useEvents` | ✅ | One connection; patterns are matched client-side. Duplicate ids suppressed. |
| frontend: snapshot event-boundary buffering | ✅ | `useSnapshot` buffers before the request, then applies only events above `asOfEventId`. |
| frontend: reset handling — reload bootstrap and mounted snapshots | ✅ | Delivery pauses, bootstrap reloads, the stream reopens, every snapshot reloads. |
| frontend: Events screen | ✅ | Pattern filter, live log, durable subscriber health with retry/skip. |

## Milestone 3 — `policy` ✅

| Feature | State | Notes |
|---|---|---|
| Plugin enabled state, registration, automated-plugin daily-budget invariant | ✅ | `internal/core/policy` |
| `CheckWork` / `CheckWorkTx` admission | ✅ | Unknown IDs denied as disabled. |
| Atomic micro-USD reserve / settle / release | ✅ | Reservations pin UTC hour/day/month; concurrent holds cannot share capacity. |
| Budget windows (hour, day, month) and `on_exceed` reject or disable | ✅ | Domain rejection commits its events; one `budget_exceeded` per plugin/window/period. |
| `Watch` cancellation of admitted contexts | ✅ | `AfterCommit` fires watchers after a caller-owned disable (kill switch, `ExceedDisable`, accounting invariant). |
| Accounting-invariant violation handling | ✅ | Truthful charge is recorded; plugin disabled; AI route reads `accountingFailed`. |
| Orphaned reservations at startup | ✅ | Settled at reserved maximum so a crash cannot silently undercount. |
| Plugins screen: kill switch, budgets, health, schema-backed config | ✅ | `/api/admin/plugins`; disabled plugins stay listed. |

## Milestone 4 — `jobs` ✅

| Feature | State | Notes |
|---|---|---|
| Enqueue with policy check in the insert transaction | ✅ | Idempotency key is unique among nonterminal rows. |
| Claim, leases, fencing, heartbeat | ✅ | Expired leases are stolen with a new generation. |
| Retries, permanent failure, timeout, panic recovery | ✅ | `failed` is permanent; `dead` is retry exhaustion. |
| Cooperative cancel and plugin-disable | ✅ | Pending jobs cancel immediately; running jobs are fenced. |
| Cron without catch-up | ✅ | Disabled ticks are dropped; DST slot is local wall time. |
| Progress and batched logs | ✅ | Events are UI invalidations; REST is the snapshot. |
| Jobs screen and dashboard running list | ✅ | `/api/jobs`; cancel from the UI. |

## Milestone 5 — `credentials` + `ai` ✅

| Feature | State | Notes |
|---|---|---|
| AES-256-GCM envelopes; startup fails closed on a missing or wrong master key | ✅ | `CC_MASTER_KEY` is 64 hex characters. |
| API-key create / replace / rotate; Admin never returns secrets | ✅ | Actor stamped from the session; mutations require reauth. |
| OAuth authorization-code + PKCE S256; state bound to session; consume-once | ✅ | Callback is a top-level GET; SameSite=Lax carries the session. |
| Credential references block deletion | ✅ | `ai.providers` is republished on every provider reload. |
| Host-managed AI routing, reserve, settle, `core.ai.usage` | ✅ | In-process `fake` provider for local use and tests. |
| Settings and Costs screens | ✅ | Reauth UI; costs grouped plugin → job → model. Routes moved to the Models screen. |

## Milestone 6 — `pluginhost` + `host` ✅

| Feature | State | Notes |
|---|---|---|
| `host` module: Plugin, Host, Manifest, and capability packages | ✅ | Plugins depend on `host` only; core adapts. |
| Registration validates all plugins before any migrate or Init | ✅ | Invalid or colliding declarations reject `RegisterAll` as a whole. |
| Scoped facade: identity stamp + L4 admission wrappers | ✅ | `events.Scoped`, `jobs.Scoped`, `ai.Scoped`, `storage.Prefixed` + authorizer. |
| SQLite authorizer on plugin SQL and migrations | ✅ | Catalog writes for DDL; `core_*` and other namespaces denied. |
| Lifecycle: migrate while disabled; Init only when enabled | ✅ | Disabled plugins stay behind host 503 / durable discard-ack. |
| Enable / Disable serialize per plugin; policy is desired state | ✅ | Incomplete convergence returns `ErrIncomplete` without rolling policy back. |
| Enforcement matrix | ✅ | HTTP 503, publish/SQL/blob admission, context cancel, reads remain. |
| ConfigAdmin + JSON Schema subset; secrets rejected | ✅ | Invalid persisted config degrades without calling plugin code. |
| `/api/plugins/<id>/` host-owned; config GET/PUT under admin | ✅ | Shell bootstrap still lists only registered frontend-matched ids. |

## Milestone 7 — `hello` ✅

| Feature | State | Notes |
|---|---|---|
| Separate `plugins/hello` module depending only on `host` | ✅ | Registered from `cmd/controlcenter/plugins.go` alone. |
| Cron tick, AI chat, event, durable handler, SQL, blob, config, browser | ✅ | `hello.ticked` writes `hello_ticks`; UI lists history live. |
| Kill-switch acceptance | ✅ | Cancel running job, skip cron, 503, Chat denied, admitted Chat settles, reads remain. |
| One-command Docker image | ✅ | `docker compose up --build` — TLS, data volume, generated secrets under `/data`. |

## Milestone 8 — `browser` ✅

| Feature | State | Notes |
|---|---|---|
| `host/browser` SDK: Open, Session, Page, allowlist, sentinels | ✅ | Plugins never import Playwright/chromedp/rod. |
| Fake engine | ✅ | In-process `http.Handler` per DNS name; no sockets, no Chromium. |
| Playwright engine | ✅ | `browser.engine: playwright`; Chromium + connect-time SSRF proxy. |
| URL policy | ✅ | HTTPS only; plugin allowlist; no userinfo/IPs/ports; fail-closed. |
| Limits | ✅ | 1 session/plugin, 4 pages/session, 5 MiB document, 10 MiB resource. |
| Kill switch | ✅ | Disable rejects new I/O and closes admitted sessions; in-flight Goto fails. |
| `core.browser.denied` | ✅ | Audit row on allowlist/private-network denial. |

## Frontend — plugin dashboard ✅

Not a numbered milestone; it completes the Dashboard row of
[`docs/frontend.md`](docs/frontend.md) §"Core screens".

| Feature | State | Notes |
|---|---|---|
| `PluginModule.dashboard`: `summary`, `live`, `tile`, `detail` | ✅ | All optional. A plugin that contributes nothing still gets a host-built tile. |
| Registration rejects an unmatchable `live` pattern | ✅ | A silently wrong live indicator is worse than none. `web/src/shell/registry.ts` |
| Dashboard grid: one live tile per plugin | ✅ | State, spend against daily budget, open and failed work, activity, plugin surface. |
| Plugin surfaces render behind an error boundary | ✅ | A broken tile never costs the operator the page or the kill switch. |
| Plugin detail at `/plugins/<id>` | ✅ | Live activity, plugin surface, jobs, event feed, spend, budget, config, kill switch. |
| Controls shared by the list and detail screens | ✅ | `web/src/core/PluginControls.tsx`; one implementation of the kill switch. |
| `useStreamStatus` / `useActivity` on the one shared stream | ✅ | Connection state comes from the `EventSource`; activity holds timestamps, never payloads. |
| Liveness needs both a declaration and a live connection | ✅ | Reconnecting turns every indicator off rather than leaving it pulsing. |
| Jobs screen filters by plugin from the URL | ✅ | `/jobs?plugin=<id>`, so a detail screen can link to its own queue. |
| Plugin tiles and detail panels | ✅ | `hello` and `pagewatch` fold complete plugin events into snapshots; no polling or refetch per event. |

## Provider administration and subscription auth ✅

Not a numbered milestone: it replaces the compiled-in half of milestone 5's routing with
administrator-owned providers, live model discovery, and a second way to authorize.

| Feature | State | Notes |
|---|---|---|
| Providers are database rows, created and edited from the UI | ✅ | `core_ai_providers`; `config/models.yaml` seeds an empty install and is ignored after. |
| Model discovery per provider, cached until refreshed | ✅ | `core_ai_catalog`; OpenRouter's published prices are converted, an unpriced model stays unpriced rather than free. |
| Routes are editable at runtime; a broken one is reported, not fatal | ✅ | Kept with `lastError`, unhealthy in the UI, `ErrRouteUncompiled` on dispatch. |
| Route attempts record the price they were admitted at | ✅ | A catalog refresh cannot silently reprice an admitted call. |
| Subscription billing reserves zero and settles zero | ✅ | A computed bound, not a missing one; the attempt row records `billing_state = subscription`. |
| Codex adapter: SSE `/responses`, account id header, `/models` | ✅ | Refuses to dispatch without the account id from the credential's `id_token`. |
| ChatGPT OAuth with a pinned loopback redirect, completed by paste | ✅ | State, session binding, PKCE, and single use are all still enforced server-side. Reauthentication is spent starting the flow, not finishing it. |
| The pinned path completes itself when this server answers there | ✅ | Only when the request's own address is the pinned one; anywhere else it is a frontend route and the state is untouched. |
| Token import from an existing local `codex login` | ✅ | Refresh token required; the shared-rotation caveat is stated in the UI. |
| Models screen: providers, catalogs, route editor | ✅ | Attempts pick a discovered model, prices prefill, the reservation is shown before saving. |

## Milestone 9 — `notifications` ✅

| Feature | State | Notes |
|---|---|---|
| Durable `core.notifications` subscriber, `FromNow` after defaults | ✅ | `SubscribeDurableTx`; `core.notification.*` never re-evaluated |
| Default rules for alerts, dead jobs, budget, accounting, reauth, paused subscribers | ✅ | Inbox channel seeded |
| Throttle windows collapse same subject; ready after window close | ✅ | Structured uniqueness keys (JSON) |
| External ntfy / webpush sends with leases, 8 attempts, credential tokens | ✅ | Inbox write never calls out |
| Admin rules/channels, credential references, inbox REST + UI | ✅ | Settings + Inbox screen |

## Milestone 10 — `tid` ✅

| Feature | State | Notes |
|---|---|---|
| Separate `plugins/tid` module depending only on `host` | ✅ | Compiled in from the one backend registration file. |
| Daily and manual energy sync | ✅ | Host-managed My TID browser flow plus CSV upload. |
| Usage metrics and insight | ✅ | Daily kWh history, month comparisons, estimated cost, and a bounded model-written insight. |
| Operator UI | ✅ | Summary metrics, history, insight, manual sync, and CSV upload. |

## Milestone 11 — `pagewatch` ✅

| Feature | State | Notes |
|---|---|---|
| Separate `plugins/pagewatch` module depending only on `host` | ✅ | Compiled in from the one backend registration file. |
| Scheduled and manual public-page check | ✅ | Every six hours; HTTPS DNS target validation; fake and Playwright browser engines. |
| Drift and expected-text results | ✅ | Baseline, unchanged, changed, and attention states; latest normalized snapshot stored as a blob. |
| Bounded AI use | ✅ | 64 output tokens; changed/attention checks plus at most one summary per day while unchanged. |
| Transactional signal path | ✅ | State and completion/alert events commit together; durable subscriber keeps 100 history rows. |
| Operator UI | ✅ | Current result, target, timings, tokens, cost, history, snapshot download, and manual trigger. |
| End-to-end acceptance test | ✅ | Real registry, policy, jobs, browser, AI accounting, blob, SQL, events, durable delivery, and HTTP. |

## Milestone 12 — `bidrl` ✅

| Feature | State | Notes |
|---|---|---|
| Separate `plugins/bidrl` module depending only on `host` | ✅ | Compiled in from the one backend registration file. |
| User-triggered collect, scan, reprice, and bid refresh | ✅ | Enqueue-only jobs; `Automated: false`; compiled HTTPS host allowlist. |
| Identification basis gates valuation | ✅ | Numeric prices only for `exact_text` / `barcode` with a cited source. |
| Operator UI | ✅ | Treasure-hunting feed, auction view, lot detail with evidence. Declared AI needs are assigned from the plugin screen. |
| End-to-end acceptance test | ✅ | Fake BIDRL site, vision + grounded-price routes, collect → scan → feed. |

---

## Deferred by design (not v1)

Agentic harness sessions · terminal visibility in the browser · externally reachable
OpenAI-compatible gateway · out-of-process or containerized plugins · plugin permissions,
installer, and registry · multi-user and multi-host. See [`DESIGN.md`](DESIGN.md) §1.
