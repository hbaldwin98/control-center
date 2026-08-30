# Implementation status

Tracks [`DESIGN.md`](DESIGN.md) §8 build order. Updated with every milestone commit.

Legend: ✅ complete · 🔨 in progress · ⬜ todo

| # | Milestone | State | Done when |
|---|---|---|---|
| 1 | Skeleton | ✅ | `cmd`, config, SQLite, migrations, TLS-aware HTTP server, first-run bootstrap, session auth, CSRF, React shell boot. |
| 2 | `storage` + `events` | ✅ | Transactional event insertion works; durable delivery is serial and at-least-once; SSE replay and reset work. |
| 3 | `policy` | 🔨 | Disable admission/cancellation works; persisted integer micro-USD reservations settle and release atomically. |
| 4 | `jobs` | ⬜ | Enqueue, retries, cancellation, progress, and job UI work; disabled plugins cannot admit jobs. |
| 5 | `credentials` + `ai` | ⬜ | Key replacement and OAuth work without exposing secrets; one provider records usage and settles reservations. |
| 6 | `pluginhost` + `host` | ⬜ | Registry, facade, lifecycle, enforcement matrix all pass. |
| 7 | `hello` | ⬜ | The validating plugin passes its acceptance test. |
| 8 | `notifications` | ⬜ | Channels use credential entries; rules and defaults deliver committed events. |
| 9 | `bidrl` | ⬜ | The first real plugin. |
| 10 | Future | ⬜ | Harness sessions, terminal visibility, external gateway, out-of-process plugins. |

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

## Milestone 3 — `policy` 🔨

| Feature | State | Notes |
|---|---|---|
| Plugin enabled state, registration, automated-plugin daily-budget invariant | ⬜ | |
| `CheckWork` / `CheckWorkTx` admission | ⬜ | |
| Atomic micro-USD reserve / settle / release | ⬜ | |
| Budget windows (hour, day, month) and `on_exceed` reject or disable | ⬜ | |
| `Watch` cancellation of admitted contexts | ⬜ | |
| Accounting-invariant violation handling | ⬜ | |
| Plugins screen: kill switch, budgets, health | ⬜ | |

## Deferred by design (not v1)

Agentic harness sessions · terminal visibility in the browser · externally reachable
OpenAI-compatible gateway · out-of-process or containerized plugins · plugin permissions,
installer, and registry · multi-user and multi-host. See [`DESIGN.md`](DESIGN.md) §1.
