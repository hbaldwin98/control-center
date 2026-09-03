# Control Center — Design

Status: implemented through harness sessions · 2026-09-02

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
| [`docs/plugins/pagewatch.md`](docs/plugins/pagewatch.md) | Low-cost end-to-end confidence plugin. |
| [`docs/plugins/bidrl.md`](docs/plugins/bidrl.md) | First real plugin. |

Module specs: [storage](docs/modules/storage.md) · [events](docs/modules/events.md) ·
[policy](docs/modules/policy.md) · [credentials](docs/modules/credentials.md) ·
[ai](docs/modules/ai.md) · [jobs](docs/modules/jobs.md) · [browser](docs/modules/browser.md) ·
[search](docs/modules/search.md) · [harness](docs/modules/harness.md) ·
[notifications](docs/modules/notifications.md) · [pluginhost](docs/modules/pluginhost.md)

The whole process, including the frontend, is one image: `docker compose up --build`.
Open `https://localhost:8443` (self-signed). The first-run admin password is
`/data/admin.password` in the volume (`docker compose exec control-center cat /data/admin.password`).

---

## 1. Scope

### In v1

- Web shell for one administrator, reachable on loopback or over TLS.
- Eleven core modules (below).
- A plugin host that mounts compiled-in plugins through a scoped facade.
- Live per-plugin token and cost accounting, with budgets.
- A per-plugin host-capability kill switch, enforced at execution, spending, publication,
  mutation, and browser-session admission points.
- Four plugins: `hello` (validating), `tid` (energy usage), `pagewatch`
  (operational confidence), and `bidrl` (the first real plugin).
- Administrator-owned harness sessions for configured commands, bounded output, and process stop.

### Not in v1

| Deferred | Reason |
|---|---|
| Interactive PTY harness sessions | The first harness slice deliberately has no stdin, resize, attach/detach, or terminal emulation protocol. |
| Interactive terminal in the browser | Requires the PTY protocol above. Retained stdout and stderr are already visible. |
| Externally reachable OpenAI-compatible gateway | Nothing outside the control center calls it yet. Additive later. |
| Out-of-process / containerized plugins | Plugins are compiled in. The current boundary is compatible with this direction, but isolation requires protocols, adapters, supervision, and capability proxies. |
| Plugin permissions, installer, registry | Single author. Those protect hosts from strangers. |
| Multi-user, multi-host | One user, one box. |

---

## 2. Principles

1. **The host owns capabilities. Plugins own workflows.** If two plugins would need it, it is
   a capability. If it is a process or network boundary the kill switch must own, it is a
   capability even with one consumer — that is why browser is host-managed from v1.
   Ordinary libraries stay in the plugin until a second one asks.
2. **Plugin identity is threaded through every host call.** One mechanism delivers cost
   attribution, the kill switch, budgets, and audit.
3. **Modules emit events; they do not call each other sideways.** The bus is the spine.
4. **Enforcement lives at host capability boundaries.** Disabling a plugin rejects new
   host-managed work, AI dispatches, browser sessions, search queries, event handlers, HTTP requests,
   event publications, and storage/blob mutations, and cancels admitted contexts. Browser
   sessions are closed, not asked to finish. Reads and diagnostic logs remain available.
   Trusted in-process code can ignore cancellation or use direct networking; hard
   termination of those paths requires out-of-process isolation.
5. **Structure in-process plugins as if they were remote.** Separate modules, a facade,
   no reach-through.

---

## 3. The layer rule

There is one structural rule, and it replaces any dependency matrix:

> **A module may import from strictly lower layers only. Never from a sibling.**

```
  L6  web            HTTP, SSE, auth, static shell
       │
  L5  plugins        hello · tid · pagewatch · bidrl    (own Go modules)
       │
  L4  pluginhost     registry · lifecycle · Host facade construction
       │
  L3  capabilities   ai · jobs · browser · search · notifications
       │
  L2  support        policy · credentials
       │
  L1  spine          events
       │
  L0  foundation     storage
```

Siblings never import each other, which is what keeps the layers real:

- `ai`, `jobs`, `browser`, and `search` do not know about each other. All four use `policy`.
- No capability module calls notifications to send. It subscribes to `events`, its web
  admin surface manages rules/channels, and it reads channel secrets through the
  lower-layer `credentials` interface.
- `credentials` is used by `ai`, `notifications`, and its web administration surface.
  Plugins and browser clients never receive a secret.
- `policy` is the only module that knows whether a plugin is allowed to act.

### Why `policy` exists

`ai` and `jobs` both need to ask "is this plugin allowed to do this right now?", and
`pluginhost` needs to answer it. Putting that answer in `pluginhost` creates a cycle:
`pluginhost → ai → pluginhost`.

So plugin state, budgets, spend counters, and the gate are extracted into `policy` at L2.
`ai`, `jobs`, `browser`, `search`, and `pluginhost` all depend on it; it depends on none of them.

---

## 4. Module map

| Layer | Module | Responsibility (one sentence) |
|---|---|---|
| L0 | [storage](docs/modules/storage.md) | SQLite handles, migrations, and filesystem blob storage. |
| L1 | [events](docs/modules/events.md) | Insert events transactionally, then dispatch committed events to live and durable subscribers. |
| L2 | [policy](docs/modules/policy.md) | Own plugin enabled state and atomically reserve, settle, and release budget capacity. |
| L2 | [credentials](docs/modules/credentials.md) | Store API keys and OAuth credentials; keep tokens fresh. |
| L3 | [ai](docs/modules/ai.md) | Route logical model names to administrator-configured providers, discover what those providers serve, and record usage and cost. |
| L3 | [jobs](docs/modules/jobs.md) | Durable queue with cron, retries, cancellation, and progress. |
| L3 | [browser](docs/modules/browser.md) | Own headless browser sessions, allowlists, and teardown for plugins. |
| L3 | [search](docs/modules/search.md) | Own web lookup for plugins: admit the query, call SearXNG or the fake engine, return public HTTPS hits. |
| L3 | [notifications](docs/modules/notifications.md) | Turn events into deliveries via rules and channels, using credential entries for channel secrets. |
| L4 | [pluginhost](docs/modules/pluginhost.md) | Register plugins, own validated plugin config, run lifecycle, and build each scoped `Host`. |

---

## 5. How it fits together

Two walkthroughs. If these read cleanly, the architecture is doing its job.

### A. A user-triggered BIDRL scan

```
 1  user → plugin HTTP     POST /api/plugins/bidrl/scans
 2  plugin → jobs          enqueue "bidrl.scan"
 3  jobs → policy          admit work for "bidrl"             → ok
 4  plugin handler         host.Browser().Open(...)           → session, allowlisted
                           page.Goto(auctionURL); page.Get(imageURL)
 5  plugin handler         host.AI().Chat(...)
                           ↳ facade stamps plugin ID onto the context
 5  ai → policy            atomically reserve conservative maximum cost
 6  ai → credentials       Token("google-api")
 7  ai                     resolve "cheap-vision" → provider → call
 8  ai + policy + events   atomically finalize call/attempt rows, settle actual
                           micro-USD, release unused reservation, and insert core.ai.usage
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
 3  policy                 commits state and core.plugin.disabled together
 4  host                   rejects new jobs, AI dispatches, browser sessions,
                           search queries, event handlers, HTTP requests, event
                           publications, and storage/blob mutations; cancels
                           admitted contexts; closes admitted browser sessions
 5  new AI request         admission → ErrPluginDisabled, no provider call
 6  admitted AI request    may finish; usage and actual cost are still recorded
 7  new Browser().Open     admission → ErrPluginDisabled, no engine call
 8  admitted browser       context cancelled, pages and session closed
 8b new Search().Query     admission → ErrPluginDisabled, no SearXNG call
 9  web                    /api/plugins/bidrl/* → 503
```

The switch controls host capabilities, not the Go process. Trusted plugin code can ignore
context cancellation or open its own network connection. Hard termination and network
containment wait for out-of-process isolation. Atomic persisted reservations prevent
concurrent paid calls from consuming the same budget capacity, including while disable is
racing with an already-admitted call.

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
    ai/  jobs/  browser/  notifications/  pluginhost/  web/
  host/                     ← separate Go module: the plugin-facing library
  plugins/
    hello/  go.mod          ← separate module
    pagewatch/  go.mod      ← separate module
    tid/  go.mod            ← separate module
  web/                      ← React/TS frontend; canonical plugin UI under src/plugins/
  config/
    models.yaml             ← seed only; providers and routes live in the database
  docs/
  DESIGN.md
```

Each plugin is its own Go module tied in by `go.work`, depending only on `host`. Because
that layout alone does not make every forbidden import illegal under Go's `internal`
rules, CI checks each plugin's full dependency graph and rejects core, application, other
plugin, and undeclared project imports. A boundary violation is therefore a failed
architectural test rather than a review convention.

---

## 7. Security and access

- There is one administrator principal and no user-management surface. First-run bootstrap
  is available only on loopback and requires a one-time token to set the admin password.
- Non-loopback access requires TLS. Passwords are hashed with Argon2id.
- Authentication uses a `__Host-` `HttpOnly`, `Secure`, `SameSite=Lax` session cookie with
  `Path=/`, no `Domain`, and 12-hour absolute and idle policies. Authentication rotates
  the session; logout invalidates it.
- Mutations require a synchronizer CSRF token bound to the session and a valid `Origin`.
  Credential changes also require password reauthentication within the last five minutes.
- Secrets live in `credentials`, encrypted at rest with a key from the environment or the
  OS keyring, never in `config/*.yaml`. Notification channel secrets are credential
  entries. Credentials administration supports API-key create/replace and secure OAuth,
  but neither its web API nor UI returns secret values. A provider that pins a redirect
  this server cannot receive is completed by pasting the address the browser landed on;
  the state, its session binding, the PKCE verifier, and single use are all still enforced
  server-side.
- Providers and model routes are configuration rather than secrets. Editing them needs a
  session and CSRF, not password reauthentication, and no route or provider response ever
  carries credential material.
- Plugins are trusted code. The table prefix and the facade are guardrails against
  mistakes, not a sandbox. Untrusted plugins are the out-of-process milestone.

---

## 8. Build order

| # | Milestone | Done when |
|---|---|---|
| 1 | Skeleton | `cmd`, config, SQLite, migrations, TLS-aware HTTP server, first-run bootstrap, session auth, CSRF, React shell boot. |
| 2 | `storage` + `events` | Transactional event insertion works; durable delivery is serial and at-least-once; SSE replay and reset work. |
| 3 | `policy` | Disable admission/cancellation works; persisted integer micro-USD reservations settle and release atomically. |
| 4 | `jobs` | Enqueue, retries, cancellation, progress, and job UI work; disabled plugins cannot admit jobs. |
| 5 | `credentials` + `ai` | Key replacement and OAuth work without exposing secrets; one provider records usage and settles reservations. |
| 6 | `pluginhost` + `host` | Registry, facade, lifecycle, enforcement matrix all pass. |
| 7 | `hello` | The [validating plugin](docs/plugin-api.md#8-the-hello-plugin) passes its acceptance test. |
| 8 | `browser` | Host-managed sessions, allowlist, fake backend, and kill-switch close all pass. |
| 9 | `notifications` | Channels use credential entries; rules and defaults deliver committed events. |
| 10 | `tid` | Daily Turlock Irrigation District usage, metrics, and insight flow through host capabilities. |
| 11 | `pagewatch` | A cheap browser-to-AI confidence plugin produces history, cost data, and actionable alerts. |
| 12 | `bidrl` | The first real plugin. |
| 13 | `push` | Host-owned topics, demand, fan-out, backpressure, and one SSE transport. |
| 14 | `harness` | Configured commands run under host supervision with durable lifecycle and bounded output. |
| 15 | Future | Interactive terminal, external gateway, out-of-process plugins. |

Steps 2–6 are the ones worth getting right. Everything after is downstream of them.

Step 7 is not optional. It is where the host API gets fixed while fixing it is still cheap.
`browser` and `pagewatch` land before `bidrl`; collection must not own Chromium, and the
complete capability pipeline gets exercised before the larger plugin depends on it.

---

## 9. V1 contract decisions

1. **Plugin config.** Manifests declare defaults and a closed JSON Schema 2020-12 subset.
   The host validates and stores one non-secret JSON document per plugin and renders the
   supported controls. Secrets remain credential references.
2. **Event retention.** Keep at least 90 days and every row pinned by an active or paused
   durable cursor. SSE receives an explicit reset when its cursor predates retained data.
3. **Job args.** V1 stores JSON and keeps `Args(into any) error`; enqueue validates JSON
   encoding and each handler validates its decoded domain type. A typed generic wrapper
   may be added later without changing persisted jobs.
4. **Browser.** The host owns sessions, allowlists, and teardown. Plugins pass the DNS
   names they intend to touch; they never import Playwright or an equivalent. Disable
   closes admitted sessions rather than letting navigation finish. Login is `Fill` /
   `FillCredential` / `Click` each session; there is no persistent cookie jar. SPA JSON
   that the page POSTs after login is read from `Responses`, not by giving plugins
   `Evaluate` or a raw fetch client with the in-page token.
