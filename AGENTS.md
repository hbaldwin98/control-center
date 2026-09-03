# AGENTS.md

## What this is

Control Center is a personal, self-hosted web application that runs on one Linux box
for one administrator and hosts plugins. **The host owns capabilities; plugins own
workflows.** Go backend, React/TypeScript frontend, SQLite storage, shipped as a
single Docker image.

Read [`DESIGN.md`](DESIGN.md) first — it is the map. [`STATUS.md`](STATUS.md) tracks
what is built. [`docs/plugin-api.md`](docs/plugin-api.md) is self-contained for
writing a plugin; [`docs/frontend.md`](docs/frontend.md) covers the SPA; one spec per
core module lives in [`docs/modules/`](docs/modules/).

## Layout

```
cmd/controlcenter/     main + the one file that registers plugins
internal/config/       configuration load and validation
internal/core/         the eleven core modules: storage, events, policy,
                       credentials, ai, jobs, browser, search, harness,
                       notifications, push, pluginhost, web
host/                  the public plugin-facing module (own go.mod):
                       host, host/ai, host/browser, host/events, host/jobs,
                       host/policy, host/search, host/storage; host/hosttest
plugins/               hello, pagewatch, tid, bidrl — one Go module each
web/src/shell/         layout, nav, auth, router, SSE client
web/src/ui/            shared design system — the ONLY thing plugin UI imports
web/src/core/          dashboard, jobs, events, costs, settings, plugin admin
web/src/plugins/<id>/  plugin UI; web/src/plugins.ts is the only importer
config/, deploy/, docs/, scripts/
```

This is a Go **workspace** (`go.work`): the app module, `host`, `host/hosttest`, and
each plugin are separate modules.

## Commands

```
docker compose up --build          # whole thing; https://localhost:8443 (self-signed)
docker compose exec control-center cat /data/admin.password   # first-run password

go build ./... && go test ./...    # root module only — see below
cd web && npm run dev              # Vite on :5173, proxies to the Go process
cd web && npm test                 # vitest
cd web && npm run typecheck        # tsc --noEmit
cd web && npm run build            # typecheck + vite build into web/dist
scripts/coverage.sh [go|web]       # coverage profiles for the CRAP analyzer
```

`go test ./...` from the root covers the root module **only**. Every module in
`go.work` must be run on its own — `.`, `host`, `host/hosttest`, and each
`plugins/*` — or its packages score as untested. `scripts/coverage.sh` does this
correctly; copy its module list rather than inventing one.

## Project rules

### Plugins stay out of the core app

Plugins must remain portable. The core app must never reference a plugin —
no imports, no function calls, no type references, no name checks, no
registry entries hardcoded to a specific plugin.

The dependency only points one way: plugins call into the core app and use
what it exposes. If the core needs behavior from a plugin, the core defines
an interface or extension point and the plugin registers itself against it.

This applies on both sides of the stack — Go (`internal/`, `host/` vs.
`plugins/`) and web (`web/src/ui/`, core modules vs. `web/src/plugins/`).

`internal/core/pluginhost/boundary_test.go` enforces the Go side over each
plugin's full `go list -deps` output: no `internal/core`, no other plugin, no
application-module import outside `host`. `go.work` and Go's `internal` rule are
not sufficient by themselves.

### Design principles that constrain changes

- **The host owns capabilities. Plugins own workflows.** If two plugins would need
  it, it is a capability. If it is a process or network boundary the kill switch
  must own, it is a capability even with one consumer.
- **Modules emit events; they do not call each other sideways.** The bus is the spine.
- **Plugin identity is threaded through every host call** — that one mechanism
  delivers cost attribution, the kill switch, budgets, and audit.
- **Enforcement lives at host capability boundaries.** Disabling a plugin rejects
  new host-managed work and cancels admitted contexts; reads and diagnostics remain.
- Costs are persisted as integer micro-USD reservations that settle atomically.

### Frontend contract

A plugin UI is one entry point, `web/src/plugins/<id>/index.tsx`, exporting a
`PluginModule` (nav, routes, dashboard tile/detail) and importing only `@cc/ui`
and its own directory. `web/src/plugins.ts` is the only file that imports plugin
modules. Backend manifests carry no routes, navigation, icons, or frontend paths.

### Conventions

- Config holds no secrets by construction; credentials go through the
  `credentials` module and are encrypted with the master key.
- Loopback may serve HTTP; anything else requires TLS (enforced in `Validate`).
- Migrations are per-namespace, run on every start, and are checksummed — editing
  a landed migration fails startup. Add a new one.
- Update `STATUS.md` with milestone commits.
