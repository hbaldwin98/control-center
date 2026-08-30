# Control Center — Design

Status: draft · 2026-08-30 · pre-implementation

A personal, self-hosted web application that runs on one Linux box and hosts plugins.
**The host owns capabilities; plugins own workflows.**

This file is the map. It covers what the system is, how the pieces are layered, and how a
request moves through them. Each module and contract is specified in its own document,
linked below.

Language assumption: **Go** backend, **React/TypeScript** frontend, **SQLite** storage.
The interfaces translate directly to Rust if that changes; the layering does not.

---

## Documents

| Document | For |
|---|---|
| This file | The overall shape. Read first. |
| [`docs/plugin-api.md`](docs/plugin-api.md) | Everything needed to write a plugin. Self-contained. |
| [`docs/frontend.md`](docs/frontend.md) | Shell, plugin UI contract, live data. |
| [`docs/modules/`](docs/modules/) | One spec per core module. |
| [`docs/plugins/bidrl.md`](docs/plugins/bidrl.md) | First real plugin. |

Module specs: [storage](docs/modules/storage.md) · [events](docs/modules/events.md) ·
[policy](docs/modules/policy.md) · [credentials](docs/modules/credentials.md) ·
[ai](docs/modules/ai.md) · [jobs](docs/modules/jobs.md) ·
[notifications](docs/modules/notifications.md) · [pluginhost](docs/modules/pluginhost.md)

---

## 1. Scope

### In v1

- Web shell, single user, private network.
- Eight core modules (below).
- A plugin host that mounts compiled-in plugins through a scoped facade.
- Live per-plugin token and cost accounting, with budgets.
- A global per-plugin kill switch, enforced by the host.
- Two plugins: `hello` (validating) and `bidrl` (real).

### Not in v1

| Deferred | Reason |
|---|---|
| Agentic harness sessions (Codex/Claude Code in a PTY) | Large surface: PTY supervision, scrollback, attach/detach, per-harness hook wiring. |
| Terminal visibility into agents in the browser | Depends on the above. |
| Externally reachable OpenAI-compatible gateway | Nothing outside the control center calls it yet. Additive later. |
| Out-of-process / containerized plugins | Plugins are compiled in, but structured so the move is a transport swap. |
| Plugin permissions, installer, registry | Single author. Those protect hosts from strangers. |
| Multi-user, multi-host | One user, one box. |

---

## 2. Principles

1. **The host owns capabilities. Plugins own workflows.** If two plugins would need it, it is
   a capability. If one plugin needs it, it lives in that plugin until a second one asks.
2. **Plugin identity is threaded through every host call.** One mechanism delivers cost
   attribution, the kill switch, budgets, and audit.
3. **Modules emit events; they do not call each other sideways.** The bus is the spine.
4. **Enforcement lives in the host, never in the plugin.** A plugin that ignores its own
   disabled flag must still be unable to spend money or start work.
5. **Structure in-process plugins as if they were remote.** Separate modules, a facade,
   no reach-through.

---

## 3. The layer rule

There is one structural rule, and it replaces any dependency matrix:

> **A module may import from strictly lower layers only. Never from a sibling.**

```
  L6  web            HTTP, SSE, auth, static shell
       │
  L5  plugins        hello · bidrl              (own Go modules)
       │
  L4  pluginhost     registry · lifecycle · Host facade construction
       │
  L3  capabilities   ai · jobs · notifications
       │
  L2  support        policy · credentials
       │
  L1  spine          events
       │
  L0  foundation     storage
```

Siblings never import each other, which is what keeps the layers real:

- `ai` and `jobs` do not know about each other. Both use `policy`.
- `notifications` calls nothing and is called by nothing. It subscribes to `events`.
- `credentials` is used only by `ai`. Plugins never see a token.
- `policy` is the only module that knows whether a plugin is allowed to act.

### Why `policy` exists

`ai` and `jobs` both need to ask "is this plugin allowed to do this right now?", and
`pluginhost` needs to answer it. Putting that answer in `pluginhost` creates a cycle:
`pluginhost → ai → pluginhost`.

So plugin state, budgets, spend counters, and the gate are extracted into `policy` at L2.
`ai`, `jobs`, and `pluginhost` all depend on it; it depends on none of them.

---

## 4. Module map

| Layer | Module | Responsibility (one sentence) |
|---|---|---|
| L0 | [storage](docs/modules/storage.md) | SQLite handles, migrations, and filesystem blob storage. |
| L1 | [events](docs/modules/events.md) | Persist every event, dispatch to live and durable subscribers. |
| L2 | [policy](docs/modules/policy.md) | Own plugin enabled state, budgets, and spend; answer "may this plugin act?" |
| L2 | [credentials](docs/modules/credentials.md) | Store API keys and OAuth credentials; keep tokens fresh. |
| L3 | [ai](docs/modules/ai.md) | Route logical model names to providers, record usage and cost. |
| L3 | [jobs](docs/modules/jobs.md) | Durable queue with cron, retries, cancellation, and progress. |
| L3 | [notifications](docs/modules/notifications.md) | Turn events into deliveries via rules and channels. |
| L4 | [pluginhost](docs/modules/pluginhost.md) | Register plugins, run their lifecycle, build their scoped `Host`. |

---

## 5. How it fits together

Two walkthroughs. If these read cleanly, the architecture is doing its job.

### A. A scheduled BIDRL scan

```
 1  jobs scheduler         cron tick for "bidrl.scan"
 2  jobs → policy          Gate.CheckWork("bidrl")            → ok
 3  jobs                   enqueue, worker picks it up, builds jobs.Context
 4  plugin handler         host.AI().Chat(...)
                           ↳ facade stamps plugin ID onto the context
 5  ai → policy            Gate.CheckSpend("bidrl", estimate) → ok
 6  ai → credentials       Token("google-api")
 7  ai                     resolve "cheap-vision" → provider → call
 8  ai → policy            Spend("bidrl", $0.0031)
 9  ai → events            publish core.ai.usage
10  web                    SSE pushes it; the cost ticks up in the UI
11  plugin handler         host.Events().Publish("deal_found", ...)
12  notifications          rule matches bidrl.deal_found → push
```

Note what never happens: the plugin never sees a credential, never names a provider, never
calls `notifications`, and never reports its own cost.

### B. Hitting the kill switch mid-scan

```
 1  user                   toggles bidrl off
 2  pluginhost → policy    Admin.Disable("bidrl", reason)
 3  policy                 writes state, publishes core.plugin.disabled
 4  jobs                   cancels running bidrl job contexts;
                           scheduler skips future bidrl ticks
 5  straggler goroutine    host.AI().Chat(...)
 6  ai → policy            Gate.CheckSpend → ErrPluginDisabled
                           ↳ rejected before any provider call
 7  web                    /api/plugins/bidrl/* → 503
```

Step 6 is the point of the whole arrangement: even code that ignored cancellation cannot
spend money.

---

## 6. Repository layout

```
control-center/
  go.work
  cmd/controlcenter/
    main.go
    plugins.go              ← the only file importing plugin packages
  internal/core/
    storage/  events/  policy/  credentials/
    ai/  jobs/  notifications/  pluginhost/  web/
  host/                     ← separate Go module: the plugin-facing library
  plugins/
    hello/  go.mod          ← separate module
    bidrl/  go.mod          ← separate module
  web/                      ← React/TS frontend
  config/
    models.yaml  channels.yaml
  docs/
  DESIGN.md
```

Each plugin is its own Go module tied in by `go.work`, depending only on `host`. Core
internals live under `internal/`. A plugin reaching into core is therefore a build error,
not a review comment.

---

## 7. Security and access

- Bound to a private network interface (tailnet). Not exposed publicly.
- Single user. Session cookie, `SameSite=Lax`, CSRF token on mutating requests.
- Secrets live in `credentials`, encrypted at rest with a key from the environment or the
  OS keyring — never in `config/*.yaml`, which is checked in.
- Plugins are trusted code. The table prefix and the facade are guardrails against
  mistakes, not a sandbox. Untrusted plugins are the out-of-process milestone.

---

## 8. Build order

| # | Milestone | Done when |
|---|---|---|
| 1 | Skeleton | `cmd`, config, SQLite, migrations, HTTP server, session auth, React shell boot. |
| 2 | `storage` + `events` | Events persist, patterns match, `/api/stream` streams, event log renders. |
| 3 | `jobs` | Cron fires, retries work, cancel works, progress streams, job UI renders. |
| 4 | `policy` | Enable/disable persists and emits; budgets tracked; `Gate` returns real answers. |
| 5 | `credentials` + `ai` | One provider adapter end to end, logical routing, usage rows, gate wired. |
| 6 | `pluginhost` + `host` | Registry, facade, lifecycle, enforcement matrix all pass. |
| 7 | `hello` | The [validating plugin](docs/plugin-api.md#8-the-hello-plugin) passes its acceptance test. |
| 8 | `notifications` | Channels, rules, defaults. |
| 9 | `bidrl` | The first real plugin. |
| 10 | Future | Harness sessions, terminal visibility, external gateway, out-of-process plugins. |

Steps 2–6 are the ones worth getting right. Everything after is downstream of them.

Step 7 is not optional. It is where the host API gets fixed while fixing it is still cheap.

---

## 9. Open questions

1. **Plugin config schema.** `Host.Config()` needs a declaration format so the settings UI
   can render a form. JSON Schema in the manifest is the obvious answer; confirm first.
2. **Event retention.** Unbounded `core_events` growth is fine for a year and then is not.
   Decide a per-type retention policy, or a global window with pinned types.
3. **Grounded search abstraction.** `Grounded bool` may be too thin — providers differ on
   citations, query count, and billing. Revisit once one is actually wired.
4. **Job args typing.** `Args(into any) error` is untyped at the boundary. Generics could
   make `jobs.Def` type-safe per handler, at some cost in declaration ergonomics.
