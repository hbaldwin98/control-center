# jobs

**Layer 3** · `internal/core/jobs` · imports `storage`, `events`, `policy` ·
used by `pluginhost`, `web`

**Responsibility.** Run work durably with at-least-once delivery, leases, retries,
cancellation, and fenced progress.

---

## Interface

```go
package jobs

type Def struct {
    Name        string
    Handler     Handler
    Schedule    string        // standard five-field cron; empty means enqueue-only
    TimeZone    string        // IANA name; required when Schedule is set
    Timeout     time.Duration // default 30m
    MaxAttempts int           // default 3
    Backoff     BackoffPolicy
    Concurrency int           // per plugin and job name; default 1
}

type Handler func(jc Context) error

type Context interface {
    context.Context

    JobID() int64
    Attempt() int
    Args(into any) error

    Progress(fraction float64, message string) error
    Logf(format string, args ...any) error
}

type Jobs interface {
    Enqueue(ctx context.Context, name string, args any, opts ...Opt) (int64, error)
    Cancel(ctx context.Context, id int64) error
    Get(ctx context.Context, id int64) (*Job, error)
    List(ctx context.Context, f Filter) ([]Job, error)
}

func WithRunAt(t time.Time) Opt
func WithIdempotencyKey(k string) Opt
func WithPriority(p int) Opt
```

`Progress` and `Logf` return a lease/fence error when the worker no longer owns the
attempt. Handlers should stop when either fails.

---

## States and delivery

```
pending ──claim──▶ running ──┬──▶ succeeded
                            ├──▶ retry_wait ──▶ pending
                            ├──▶ failed
                            ├──▶ dead
                            └──▶ cancel_requested ──▶ cancelled

pending / retry_wait ──cancel──▶ cancelled
```

Delivery is at least once. A worker can perform side effects and lose its lease before
recording success; another worker may then run the same job. Handlers must make external
side effects idempotent. Fencing protects job-owned rows and events, not arbitrary
external systems.

Each claim transaction selects an eligible row, rechecks `policy.Gate.CheckWorkTx`, checks
the concurrency limit, increments `attempt` and `fence_generation`, and writes
`lease_owner` and `lease_expires_at`. A heartbeat extends the lease only where job ID,
owner, generation, and `running` state still match. An expired lease is claimable by a
new worker with a new generation.

Every progress, log, event, retry, and terminal write includes the worker's owner and
generation predicate. A stale worker therefore cannot update the job after lease loss,
cancellation fencing, or a replacement claim. Host APIs propagate the current job ID and
fence so job-attributed writes can apply the same check.

---

## Policy checks

- Manual `Enqueue` rechecks persisted policy state in the insertion transaction.
- Cron rechecks policy in its slot/insertion transaction; disabled ticks are dropped, not
  backlogged.
- The claim transaction checks policy again immediately before admitting execution.
- Disable stops new claims and cancels contexts for admitted attempts.

The checks at enqueue and claim are both required because policy may change while a job
waits. They revoke host-managed entrypoints; they do not isolate the Go process or stop a
handler's direct networking. Enqueue, cron insertion, and claim use `CheckWorkTx` in the
same write transaction as the job change, so each operation linearizes before or after a
concurrent disable rather than between a detached check and write.

---

## Cancellation, timeout, and failure

Cancellation is cooperative. Cancelling a running job first changes it to
`cancel_requested`, cancels its context, and gives the handler a bounded grace period.
When the handler returns, or the grace period expires, the worker conditionally fences
the lease and records `cancelled` with reason `user`, `plugin_disabled`, or `shutdown`.
Go cannot kill a handler goroutine: code that ignores cancellation may continue, but its
stale progress, logs, and terminal writes are rejected. Existing HTTP/provider I/O only
stops if it honors context cancellation.

A timeout cancels the context and is classified as a failed attempt. It enters
`retry_wait` when attempts remain, otherwise `dead`. The host may fence and move on after
the grace period even if the handler goroutine remains. Shutdown cancellation leaves the
lease to expire rather than consuming an attempt when ownership cannot be cleanly
finished.

The worker recovers handler panics, persists the panic class and stack in the job log,
and handles the attempt as a retryable failure. Ordinary handler errors are retryable by
default. `jobs.Permanent(err)` records `failed` without retry; `jobs.RetryAfter(err, d)`
overrides backoff. Invalid arguments and declared validation errors are permanent.
Context cancellation caused by user cancellation or plugin disable is `cancelled`, never
retried. Policy errors are not retried while policy remains disabled or over budget.

Every enqueue, claim, retry, cancellation, and terminal transition inserts its required
event with `events.PublishTx` in the same transaction as the state change. `core.job.failed`
fires for every failed attempt. `core.job.dead` fires once after the
last retryable attempt. `failed` is terminal for a permanent error; `dead` means retry
exhaustion.

Progress writes are persisted and emit `core.job.progressed` at most once per job per
second. Log appends are batched and emit `core.job.logged` with the highest appended log
ID, not the log text. These events are UI invalidations; REST remains the source of the
complete progress and log snapshot.

---

## Idempotency and cron

An idempotency key is scoped to `(plugin_id, job_name, key)`. A partial unique index over
nonterminal states (`pending`, `running`, `retry_wait`, `cancel_requested`) makes
concurrent enqueue atomic; a conflicting enqueue returns the existing job ID. The key may
be reused after that job reaches a terminal state.

Cron accepts standard five-field `minute hour day-of-month month day-of-week` syntax,
with no seconds or aliases. `TimeZone` must be an explicit IANA zone. The scheduler stores
the last considered schedule slot and never catches up missed slots after downtime or
disable. A nonexistent local time during the spring DST jump is skipped; a repeated local
time during the autumn fallback runs once, identified by its local schedule fields and
zone.

---

## Events emitted

| Event | Payload |
|---|---|
| `core.job.enqueued` | job ID, plugin, name, args digest |
| `core.job.started` | job ID, plugin, name, attempt, fence generation |
| `core.job.progressed` | job ID, plugin, name, fraction, message |
| `core.job.logged` | job ID, plugin, name, through log ID |
| `core.job.succeeded` | job ID, plugin, name, attempt, duration |
| `core.job.failed` | job ID, plugin, name, attempt, error class |
| `core.job.cancelled` | job ID, plugin, name, attempt, reason |
| `core.job.dead` | job ID, plugin, name, attempts, last error |

---

## Tables

```
core_jobs(
  id, plugin_id, name, args, state, attempt, max_attempts,
  priority, idempotency_key, run_at, next_retry_at,
  lease_owner, lease_expires_at, fence_generation,
  cancel_reason, started_at, finished_at,
  progress, progress_message, last_error, last_error_class, created_at
)

core_job_logs(id, job_id, attempt, fence_generation, at, line)
core_job_schedules(plugin_id, job_name, timezone, last_slot, updated_at)
```

The claim index covers `(state, run_at, next_retry_at, priority)`. Lease and terminal
updates use `(id, lease_owner, fence_generation)` predicates. The idempotency constraint
uses `(plugin_id, name, idempotency_key)` for nonterminal rows with a non-null key.
