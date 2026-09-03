# Control Center

A personal, self-hosted web application that runs on one Linux box for one
administrator and hosts plugins.

**The host owns capabilities; plugins own workflows.** Go backend, React/TypeScript
frontend, SQLite storage, shipped as a single Docker image.

## Why it exists

Small personal automations — watch a page, track a utility bill, scan an auction
site — each need the same handful of awkward things: a browser that can run
JavaScript, an AI provider with a key that must not leak, a job runner, a place to
put rows and blobs, a way to be notified. Rebuilding that per script means secrets
scattered across environments, no idea what the AI spend was, and no single switch
to stop something that has gone wrong.

Control Center builds those capabilities once, in the host, and lets each workflow
arrive as a plugin. Because plugin identity is threaded through every host call,
the host can attribute every token and dollar, enforce per-plugin budgets, and kill
a misbehaving plugin's host-managed work at the capability boundary — without the
plugin cooperating.

## What's in it

**Core modules** (`internal/core/`): storage, events, policy, credentials,
ai, jobs, browser, search, harness, notifications, push, pluginhost, web. The event
bus is the spine — modules emit events rather than calling each other sideways.

**Four plugins** (`plugins/`), each its own Go module:

| Plugin | What it does |
|---|---|
| `hello` | Validates the host boundary; the reference for writing a plugin. |
| `pagewatch` | Checks one public page every six hours, reports content drift, alerts when configured text disappears. |
| `tid` | Daily Turlock Irrigation District energy usage, metrics, and insight. |
| `bidrl` | Analyses BIDRL auction lots from their photographs rather than their titles, and surfaces lots that are anomalously cheap for what the pictures actually show. |

Costs are persisted as integer micro-USD reservations that settle atomically, so
per-plugin spend and budgets are exact rather than estimated.

## Deployment

The whole thing — frontend, daemon, and a private SearXNG sidecar — is one
`docker compose` stack.

```sh
docker compose up --build
```

Open <https://localhost:8443> (self-signed certificate). Get the first-run admin
password out of the volume:

```sh
docker compose exec control-center cat /data/admin.password
```

On first start the entrypoint generates, into the `/data` volume, whatever was not
supplied by the environment: a master key (`/data/master.key`), the admin bootstrap
password (`/data/admin.password`), and a self-signed localhost TLS certificate
(`/data/tls/`). Keep that volume and all of it survives restarts. Chromium is
downloaded once into `/data/.playwright`.

The SearXNG sidecar is private — it is not published on the host. Control Center
queries its JSON API; plugins never see the URL or choose the engine.

### Configuration

`config/config.example.yaml` is the annotated template; copy it to
`config/config.yaml` and edit. **Config holds no secrets by construction** —
API keys, OAuth credentials, and notification channel secrets are credential
entries, encrypted at rest with the master key.

Secrets and deployment-specific values come from the environment instead:

| Variable | Purpose |
|---|---|
| `CC_MASTER_KEY` | 64 hex characters; encrypts every stored secret. Generated into the volume if unset. |
| `CC_BOOTSTRAP_PASSWORD` | First-run admin password. Generated into the volume if unset. |
| `CC_ADDR` | Listen address (`0.0.0.0:8443` in the image). |
| `CC_ORIGINS` | Extra acceptable `Origin` values for mutations. |
| `CC_TLS_CERT` / `CC_TLS_KEY` | PEM paths. Set both or neither. |
| `CC_DATA_DIR` | Database, blobs, key, and password location (`/data`). |
| `CC_BROWSER_ENGINE` | `fake` (in-process fixtures) or `playwright` (host-owned Chromium). |
| `CC_SEARCH_ENGINE` / `CC_SEARCH_SEARXNG_URL` | `fake` or `searxng`, and the sidecar's address. |
| `CC_OAUTH_GOOGLE_CLIENT_ID` / `_SECRET` | Enables the Google OAuth provider. |
| `CC_OAUTH_CODEX_CLIENT_ID` | Overrides the public ChatGPT client id. |

A loopback address may serve plain HTTP; **any other address requires TLS**, and
that is enforced in config validation, not by convention.

Migrations are per-namespace, run on every start, and are checksummed — editing a
migration that has already landed fails startup. Add a new one.

### Running outside Docker

Nothing requires the container, but you supply what the entrypoint would have:
a `CC_MASTER_KEY`, a bootstrap password, and either a loopback `addr` or a TLS
keypair.

```sh
cd web && npm run build       # into web/dist
go build ./cmd/controlcenter
./controlcenter --config config/config.yaml --static web/dist
```

## Development

```sh
go build ./... && go test ./...    # root module only — see below
cd web && npm run dev              # Vite on :5173, proxies to the Go process
cd web && npm test                 # vitest
cd web && npm run typecheck        # tsc --noEmit
cd web && npm run build            # typecheck + vite build into web/dist
scripts/coverage.sh [go|web]       # coverage profiles for the CRAP analyzer
```

This is a Go **workspace** (`go.work`): the app module, `host`, `host/hosttest`,
and each plugin are separate modules. `go test ./...` from the root covers the root
module only — every module must be run on its own, or its packages score as
untested. `scripts/coverage.sh` does this correctly; copy its module list rather
than inventing one.

### Layout

```
cmd/controlcenter/     main + the one file that registers plugins
internal/config/       configuration load and validation
internal/core/         the core modules
host/                  the public plugin-facing module (own go.mod)
plugins/               hello, pagewatch, tid, bidrl — one Go module each
web/src/shell/         layout, nav, auth, router, SSE client
web/src/ui/            shared design system — the ONLY thing plugin UI imports
web/src/core/          dashboard, jobs, events, costs, settings, plugin admin
web/src/plugins/<id>/  plugin UI; web/src/plugins.ts is the only importer
config/, deploy/, docs/, scripts/
```

### The one rule that shapes everything

Plugins stay out of the core app. The dependency points one way: plugins call into
the host and use what it exposes. The core app never references a plugin — no
imports, no name checks, no hardcoded registry entries. If the core needs behavior
from a plugin, it defines an extension point the plugin registers against.

This holds on both sides of the stack, and it is tested rather than trusted:
`internal/core/pluginhost/boundary_test.go` checks each plugin's full
`go list -deps` output. On the web side, a plugin UI is one entry point,
`web/src/plugins/<id>/index.tsx`, exporting a `PluginModule` and importing only
`@cc/ui` and its own directory.

## Documentation

| Document | For |
|---|---|
| [`DESIGN.md`](DESIGN.md) | The overall shape. Read first — it is the map. |
| [`STATUS.md`](STATUS.md) | What is built, against the design's build order. |
| [`AGENTS.md`](AGENTS.md) | The authoritative project rules. |
| [`docs/plugin-api.md`](docs/plugin-api.md) | Everything needed to write a plugin. Self-contained. |
| [`docs/frontend.md`](docs/frontend.md) | Shell, plugin UI contract, live data. |
| [`docs/modules/`](docs/modules/) | One spec per core module. |
| [`docs/plugins/`](docs/plugins/) | Per-plugin notes. |

## Scope

One user, one box. Not in v1: interactive PTY harness sessions and a browser
terminal, an externally reachable OpenAI-compatible gateway, out-of-process or
containerized plugins, a plugin permission model / installer / registry, and
multi-user or multi-host operation. The current boundaries are compatible with
those directions; none of them are needed by a single author on one machine.
