# jobs

**Layer 3** · `internal/core/jobs` · imports `storage`, `events`, `policy` ·
used by `pluginhost`, `web`

**Responsibility.** Run work durably: a queue, cron scheduling, retries, cancellation, and
progress.

Almost everything a plugin does asynchronously is a job, so this is the most-used
capability in the system.

---

## Interface

```go
package jobs

type Def struct {
    Name        string        // unique within the owning plugin
    Handler     Handler
    Schedule    string        // cron expression; empty means enqueue-only
    Timeout     time.Duration // default 30m
    MaxAttempts int           // default 3
    Backoff     BackoffPolicy
    Concurrency int           // max simultaneous runs of this job; default 1
}

type Handler func(jc Context) error

type Context interface {
    context.Context           // cancelled on stop, timeout, or plugin disable

    JobID() int64
    Attempt() int
    Args(into any) error

    Progress(fraction float64, message string)
    Logf(format string, args ...any)   // persisted, streamed to the UI
}

type Jobs interface {
    Enqueue(ctx context.Context, name string, args any, opts ...Opt) (int64, error)
    Cancel(ctx context.Context, id int64) error
    Get(ctx context.Context, id int64) (*Job, error)
    List(ctx context.Context, f Filter) ([]Job, error)
}

func WithRunAt(t time.Time) Opt
func WithIdempotencyKey(k string) Opt   // dedupes against pending/running jobs
func WithPriority(p int) Opt
```

---

## Lifecycle

```
pending ──▶ running ──┬──▶ succeeded
                      ├──▶ failed ──▶ (retry) ──▶ pending
                      ├──▶ cancelled
                      └──▶ dead          retries exhausted
```

`core.job.failed` fires on every failed attempt. `core.job.dead` fires once, when retries
are exhausted, and has a default notification rule.

---

## Interaction with `policy`

Two touch points, both in this module rather than in plugin code:

1. **Before scheduling.** The cron scheduler calls `Gate.CheckWork` before enqueuing. A
   disabled plugin's ticks are *dropped*, not queued — re-enabling must not unleash a
   backlog.
2. **On state change.** A `Gate.Watch` subscription cancels the `jobs.Context` of every
   running job belonging to a plugin that has just been disabled. The job is recorded as
   `cancelled` with reason `plugin_disabled`.

A handler that ignores context cancellation is killed at its timeout. It cannot spend
anything in the meantime, because [`ai`](ai.md) checks the gate independently.

---

## Events emitted

| Event | Payload |
|---|---|
| `core.job.enqueued` | job ID, plugin, name, args digest |
| `core.job.started` | job ID, attempt |
| `core.job.succeeded` | job ID, duration |
| `core.job.failed` | job ID, attempt, error |
| `core.job.cancelled` | job ID, reason |
| `core.job.dead` | job ID, attempts, last error |

---

## Tables

```
core_jobs(id, plugin_id, name, args, state, attempt, max_attempts,
          priority, idempotency_key, run_at, started_at, finished_at,
          progress, progress_message, last_error, created_at)

core_job_logs(id, job_id, at, line)
```

Index on `(state, run_at, priority)` for the claim query, and `(plugin_id, created_at)`
for the UI.

---

## Notes

- **A single worker pool** shared across plugins, with per-job `Concurrency` limits. Per
  plugin fairness is not a concern with one user.
- **Claiming is a transaction**, so a restart mid-job leaves the row claimable again after
  a lease expires rather than lost.
- `Args(into any) error` is untyped at the boundary. Making `Def` generic is an
  [open question](../../DESIGN.md#9-open-questions).
