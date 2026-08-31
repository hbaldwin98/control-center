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
| 9 | `notifications` | ⬜ | Channels use credential entries; rules and defaults deliver committed events. |
| 10 | `bidrl` | ⬜ | The first real plugin. |
| 11 | Future | ⬜ | Harness sessions, terminal visibility, external gateway, out-of-process plugins. |

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
| Credential references block deletion | ✅ | `ai.routes` is replaced from compiled `models.yaml` at startup. |
| Host-managed AI routing, reserve, settle, `core.ai.usage` | ✅ | In-process `fake` provider for local use and tests. |
| Settings and Costs screens | ✅ | Reauth UI; routes hide credential ids; costs grouped plugin → job → model. |

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
| URL policy | ✅ | HTTPS only; plugin allowlist; no userinfo/IPs/ports; fail-closed. |
| Limits | ✅ | 1 session/plugin, 4 pages/session, 5 MiB document, 10 MiB resource. |
| Kill switch | ✅ | Disable rejects new I/O and closes admitted sessions; in-flight Goto fails. |
| `core.browser.denied` | ✅ | Audit row on allowlist/private-network denial. |

## Deferred by design (not v1)

Agentic harness sessions · terminal visibility in the browser · externally reachable
OpenAI-compatible gateway · out-of-process or containerized plugins · plugin permissions,
installer, and registry · multi-user and multi-host. See [`DESIGN.md`](DESIGN.md) §1.
