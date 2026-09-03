**Layer 3** · `internal/core/harness` · imports `storage`, `events` · used by `web`

**Responsibility.** Run administrator-approved command profiles, supervise their process
lifecycle, and retain a bounded durable record of stdout and stderr.

---

## Boundary

Harness is administrator-owned and is not part of the plugin facade. A browser request may
choose a configured profile, a title, and a workspace relative to that profile's root. It
cannot provide an executable, arguments, environment variables, or an absolute workspace.
An optional initial instruction is appended as one argument only when the profile permits it.

Profiles are deployment configuration:

```yaml
harness:
  maxSessions: 4
  maxOutputBytes: 1048576
  stopTimeout: 5s
  profiles:
    - id: codex
      name: Codex
      command: codex
      args: ["exec"]
      workspaceRoot: "/workspace"
      acceptsInstruction: true
```

The workspace path must exist. Resolution follows symlinks and then verifies that the result
is still beneath `workspaceRoot`. This is path selection, not a filesystem sandbox: the
configured process runs with the control center's operating-system identity.

## Lifecycle

```
starting ──start──▶ running ──exit 0──▶ exited
    │                  ├──exit/error──▶ failed
    │                  └──stop──▶ stopping ──exit──▶ stopped
    └──start error──▶ failed

starting / running / stopping ──server restart──▶ interrupted
```

The service reserves capacity before inserting or starting work. A start failure remains in
history as `failed`. Stop records `stopping`, sends an interrupt, and kills the process after
`stopTimeout` if it has not exited. Service shutdown stops admission, requests stop for every
live process, and waits for process and output goroutines before events and storage close.

The current implementation supervises a direct non-interactive process. PTYs, stdin, terminal
resize, attach/detach, and reconnecting to an operating-system process after restart remain
future work.

## Output

stdout and stderr writes are split into bounded chunks and inserted into
`core_harness_output`. After each insert, oldest chunks beyond `maxOutputBytes` for that
session are deleted. Detail reads return at most 2,000 newest chunks in chronological order
and mark the result when the row limit truncates it.

Output bytes never enter `core_events`. At most twice per second per live session, the module
publishes `core.harness.output` as an invalidation. The Sessions screen then reloads the REST
snapshot through the shell's shared event stream.

## HTTP API

All routes require the administrator session. POST routes also require the ordinary allowed
origin and CSRF token.

| Method | Route | Result |
|---|---|---|
| `GET` | `/api/harness` | Profiles and the newest 100 sessions. |
| `POST` | `/api/harness` | Start a session from a configured profile. |
| `GET` | `/api/harness/{id}` | Session lifecycle and retained output. |
| `POST` | `/api/harness/{id}/stop` | Request graceful stop. |

## Events emitted

| Event | Meaning |
|---|---|
| `core.harness.created` | Durable `starting` row created. |
| `core.harness.started` | Process start succeeded. |
| `core.harness.output` | Retained output changed; payload contains no output bytes. |
| `core.harness.stopping` | Stop was requested. |
| `core.harness.exited` | Process exited successfully. |
| `core.harness.failed` | Start, wait, or non-zero exit failed. |
| `core.harness.stopped` | Process exited after a stop request. |
| `core.harness.interrupted` | Startup reconciled an active row from the prior process. |

Lifecycle row changes and their events use the same SQLite transaction.

## Tables

```
core_harness_sessions(
  id, profile_id, profile_name, title, workspace, state,
  exit_code, error, stop_reason, created_at, started_at, finished_at
)

core_harness_output(id, session_id, stream, text, bytes, created_at)
```
