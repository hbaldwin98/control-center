package jobs

import (
	"context"
	"fmt"

	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

const jobSelect = `
SELECT id, plugin_id, name, args, state, attempt, max_attempts, concurrency, priority,
       idempotency_key, run_at, next_retry_at, lease_owner, lease_expires_at,
       fence_generation, cancel_reason, started_at, finished_at, progress,
       progress_message, last_error, last_error_class, created_at
  FROM core_jobs`

type jobRow struct {
	id              int64
	pluginID        string
	name            string
	args            string
	state           State
	attempt         int
	maxAttempts     int
	concurrency     int
	priority        int
	idempotencyKey  *string
	runAt           string
	nextRetryAt     *string
	leaseOwner      string
	leaseExpiresAt  *string
	fenceGeneration int64
	cancelReason    string
	startedAt       *string
	finishedAt      *string
	progress        float64
	progressMessage string
	lastError       string
	lastErrorClass  string
	createdAt       string
}

func scanJob(row interface{ Scan(dest ...any) error }) (jobRow, error) {
	var j jobRow
	var state string
	err := row.Scan(&j.id, &j.pluginID, &j.name, &j.args, &state, &j.attempt, &j.maxAttempts,
		&j.concurrency, &j.priority, &j.idempotencyKey, &j.runAt, &j.nextRetryAt,
		&j.leaseOwner, &j.leaseExpiresAt, &j.fenceGeneration, &j.cancelReason,
		&j.startedAt, &j.finishedAt, &j.progress, &j.progressMessage,
		&j.lastError, &j.lastErrorClass, &j.createdAt)
	if err != nil {
		return jobRow{}, err
	}
	j.state = State(state)
	return j, nil
}

func (j jobRow) toJob() Job {
	out := Job{
		ID:              j.id,
		PluginID:        j.pluginID,
		Name:            j.name,
		Args:            j.args,
		State:           j.state,
		Attempt:         j.attempt,
		MaxAttempts:     j.maxAttempts,
		Priority:        j.priority,
		FenceGeneration: j.fenceGeneration,
		CancelReason:    j.cancelReason,
		Progress:        j.progress,
		ProgressMessage: j.progressMessage,
		LastError:       j.lastError,
		LastErrorClass:  j.lastErrorClass,
	}
	if j.idempotencyKey != nil {
		out.IdempotencyKey = *j.idempotencyKey
	}
	out.RunAt, _ = parseTime(j.runAt)
	out.NextRetryAt = parseTimePtr(j.nextRetryAt)
	out.StartedAt = parseTimePtr(j.startedAt)
	out.FinishedAt = parseTimePtr(j.finishedAt)
	out.CreatedAt, _ = parseTime(j.createdAt)
	return out
}

// Enqueue admits one job. Policy is checked in the insertion transaction so a concurrent
// disable linearizes before or after this write, never between a detached check and insert.
func (q *Queue) Enqueue(ctx context.Context, pluginID, name string, args any, opts ...Opt) (int64, error) {
	def, ok := q.lookupDef(pluginID, name)
	if !ok {
		return 0, fmt.Errorf("%w: %s.%s", ErrUnknownDef, pluginID, name)
	}
	payload, err := marshalArgs(args)
	if err != nil {
		return 0, err
	}

	o := enqueueOpts{}
	for _, opt := range opts {
		opt(&o)
	}

	var id int64
	err = q.db.Tx(ctx, func(tx storage.Tx) error {
		if err := q.gate.CheckWorkTx(ctx, tx, pluginID); err != nil {
			return err
		}
		var e error
		id, e = q.enqueueTx(ctx, tx, pluginID, def, payload, o)
		return e
	})
	if err != nil {
		return 0, err
	}
	q.signal()
	return id, nil
}

// Cancel requests cancellation. Pending and retrying jobs become cancelled immediately.
// A running job becomes cancel_requested, its context is cancelled, and the worker
// fences the terminal write.
func (q *Queue) Cancel(ctx context.Context, id int64) error {
	return q.cancel(ctx, id, ReasonUser, "")
}

func (q *Queue) cancel(ctx context.Context, id int64, reason, requirePlugin string) error {
	err := q.db.Tx(ctx, func(tx storage.Tx) error {
		j, err := scanJob(tx.QueryRow(ctx, jobSelect+` WHERE id = ?`, id))
		if storage.IsNoRows(err) {
			return fmt.Errorf("%w: %d", ErrUnknownJob, id)
		}
		if err != nil {
			return err
		}
		if requirePlugin != "" && j.pluginID != requirePlugin {
			return ErrPluginMismatch
		}
		if j.state.Terminal() {
			if j.state == StateCancelled {
				return nil
			}
			return ErrAlreadyTerminal
		}
		now := rfc(q.now())
		switch j.state {
		case StatePending, StateRetryWait:
			if _, err := tx.Exec(ctx,
				`UPDATE core_jobs
				    SET state = ?, cancel_reason = ?, finished_at = ?
				  WHERE id = ?`,
				string(StateCancelled), reason, now, id); err != nil {
				return err
			}
			j.state = StateCancelled
			j.cancelReason = reason
			return q.publishJob(ctx, tx, events.TypeJobCancelled, j, map[string]any{
				"attempt": j.attempt, "reason": reason,
			})
		case StateRunning, StateCancelRequested:
			if _, err := tx.Exec(ctx,
				`UPDATE core_jobs SET state = ?, cancel_reason = ? WHERE id = ?`,
				string(StateCancelRequested), reason, id); err != nil {
				return err
			}
			return nil
		default:
			return ErrNotCancellable
		}
	})
	if err != nil {
		return err
	}
	q.cancelAttempt(id, reason)
	q.signal()
	return nil
}

// Get returns one job and its log lines.
func (q *Queue) Get(ctx context.Context, id int64) (*Job, error) {
	j, err := scanJob(q.db.QueryRow(ctx, jobSelect+` WHERE id = ?`, id))
	if storage.IsNoRows(err) {
		return nil, fmt.Errorf("%w: %d", ErrUnknownJob, id)
	}
	if err != nil {
		return nil, err
	}
	out := j.toJob()
	logs, err := q.logs(ctx, id, 1000)
	if err != nil {
		return nil, err
	}
	out.Logs = logs
	return &out, nil
}

// List returns matching jobs, newest first.
func (q *Queue) List(ctx context.Context, f Filter) ([]Job, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	query := jobSelect + ` WHERE 1=1`
	args := []any{}
	if f.PluginID != "" {
		query += ` AND plugin_id = ?`
		args = append(args, f.PluginID)
	}
	if f.Name != "" {
		query += ` AND name = ?`
		args = append(args, f.Name)
	}
	if f.State != "" {
		query += ` AND state = ?`
		args = append(args, string(f.State))
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, f.Limit)

	rows, err := q.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j.toJob())
	}
	return out, rows.Err()
}

func (q *Queue) logs(ctx context.Context, jobID int64, limit int) ([]LogLine, error) {
	rows, err := q.db.Query(ctx,
		`SELECT id, attempt, at, line FROM core_job_logs
		  WHERE job_id = ? ORDER BY id LIMIT ?`, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LogLine{}
	for rows.Next() {
		var l LogLine
		var at string
		if err := rows.Scan(&l.ID, &l.Attempt, &at, &l.Line); err != nil {
			return nil, err
		}
		l.At, _ = parseTime(at)
		out = append(out, l)
	}
	return out, rows.Err()
}

func (q *Queue) readJob(ctx context.Context, id int64) (jobRow, error) {
	j, err := scanJob(q.db.QueryRow(ctx, jobSelect+` WHERE id = ?`, id))
	if storage.IsNoRows(err) {
		return jobRow{}, fmt.Errorf("%w: %d", ErrUnknownJob, id)
	}
	return j, err
}
