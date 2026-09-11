package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/policy"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

func (q *Queue) claimUntilEmpty(ctx context.Context) {
	for ctx.Err() == nil {
		claimed, err := q.claimOne(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Error("jobs: claim", "err", err)
			return
		}
		if !claimed {
			return
		}
	}
}

func (q *Queue) claimOne(ctx context.Context) (bool, error) {
	now := q.now()
	nowStr := rfc(now)
	var claimed jobRow
	var owner string
	var retry bool
	err := q.db.Tx(ctx, func(tx storage.Tx) error {
		j, err := scanJob(tx.QueryRow(ctx, jobSelect+`
			 WHERE (
			         (state = 'pending' AND run_at <= ?)
			      OR (state = 'retry_wait' AND next_retry_at <= ?)
			      OR (state = 'running' AND lease_expires_at IS NOT NULL AND lease_expires_at <= ?)
			      OR (state = 'cancel_requested' AND lease_expires_at IS NOT NULL AND lease_expires_at <= ?)
			       )
			   AND (
			         SELECT count(*) FROM core_jobs r
			          WHERE r.plugin_id = core_jobs.plugin_id
			            AND r.name = core_jobs.name
			            AND r.state IN ('running', 'cancel_requested')
			            AND r.id != core_jobs.id
			       ) < core_jobs.concurrency
			 ORDER BY priority DESC, coalesce(next_retry_at, run_at) ASC, id ASC
			 LIMIT 1`, nowStr, nowStr, nowStr, nowStr))
		if storage.IsNoRows(err) {
			return errSkip
		}
		if err != nil {
			return err
		}

		// Both branches below finish the job instead of claiming it, then look for
		// another. They return nil so the transaction commits: any error rolls it back
		// and the same row would be picked again forever.

		// A cancel whose worker stopped renewing the lease will never be acknowledged.
		// Finish it here, or it holds its concurrency slot forever.
		if j.state == StateCancelRequested {
			reason := j.cancelReason
			if reason == "" {
				reason = ReasonUser
			}
			if err := q.fenceCancel(ctx, tx, j, reason); err != nil {
				return err
			}
			retry = true
			return nil
		}

		if err := q.gate.CheckWorkTx(ctx, tx, j.pluginID); err != nil {
			if errors.Is(err, policy.ErrPluginDisabled) || errors.Is(err, policy.ErrUnknownPlugin) {
				if err := q.fenceCancel(ctx, tx, j, ReasonPluginDisabled); err != nil {
					return err
				}
				retry = true
				return nil
			}
			return err
		}

		nextAttempt := j.attempt + 1
		if nextAttempt > j.maxAttempts {
			return q.fenceDead(ctx, tx, j, "lease expired with no attempts remaining", ClassError)
		}

		owner = q.opts.Owner
		fence := j.fenceGeneration + 1
		leaseUntil := rfc(now.Add(q.opts.LeaseTTL))
		res, err := tx.Exec(ctx,
			`UPDATE core_jobs
			    SET state = ?, attempt = ?, fence_generation = ?,
			        lease_owner = ?, lease_expires_at = ?,
			        started_at = COALESCE(started_at, ?),
			        cancel_reason = '', last_error = '', last_error_class = ''
			  WHERE id = ?`,
			string(StateRunning), nextAttempt, fence, owner, leaseUntil, nowStr, j.id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return errSkip
		}
		j.state = StateRunning
		j.attempt = nextAttempt
		j.fenceGeneration = fence
		j.leaseOwner = owner
		claimed = j
		return q.publishJob(ctx, tx, events.TypeJobStarted, j, map[string]any{
			"attempt": nextAttempt, "fenceGeneration": fence,
		})
	})
	if errors.Is(err, errSkip) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if retry {
		return true, nil
	}

	def, ok := q.lookupDef(claimed.pluginID, claimed.name)
	if !ok {
		_ = q.failAttempt(ctx, claimed, owner, claimed.fenceGeneration,
			Permanent(fmt.Errorf("no handler registered for %s.%s", claimed.pluginID, claimed.name)))
		return true, nil
	}
	go q.runAttempt(claimed, def, owner)
	return true, nil
}

var errSkip = errors.New("jobs: skip claim")

func (q *Queue) runAttempt(job jobRow, def Def, owner string) {
	timeoutCtx, timeoutCancel := context.WithTimeout(context.Background(), def.Timeout)
	defer timeoutCancel()
	runCtx, cancelCause := context.WithCancelCause(timeoutCtx)

	att := &runningAttempt{
		jobID:    job.id,
		pluginID: job.pluginID,
		name:     job.name,
		attempt:  job.attempt,
		fence:    job.fenceGeneration,
		owner:    owner,
		cancel:   cancelCause,
	}

	q.attMu.Lock()
	q.attempts[job.id] = att
	q.attMu.Unlock()

	defer func() {
		cancelCause(nil)
		q.attMu.Lock()
		delete(q.attempts, job.id)
		q.attMu.Unlock()
	}()

	// If Cancel raced with claim, honour it before invoking the handler.
	if current, err := q.readJob(context.Background(), job.id); err == nil && current.state == StateCancelRequested {
		q.finishCancelled(context.Background(), job, owner, job.fenceGeneration, current.cancelReason)
		return
	}

	stopBeat := q.startHeartbeat(att)
	defer stopBeat()

	jc := &jobCtx{
		Context: runCtx,
		q:       q,
		job:     job,
		owner:   owner,
		fence:   job.fenceGeneration,
		attempt: job.attempt,
	}

	var handlerErr error
	func() {
		defer func() {
			if p := recover(); p != nil {
				handlerErr = panicError{err: fmt.Errorf("panic: %v\n%s", p, debug.Stack())}
			}
		}()
		handlerErr = def.Handler(jc)
		_ = jc.flushLogs(context.Background())
	}()
	if errors.As(handlerErr, &panicError{}) {
		_ = jc.Logf("%s", handlerErr)
		_ = jc.flushLogs(context.Background())
	}

	deadlineExceeded := errors.Is(runCtx.Err(), context.DeadlineExceeded) ||
		errors.Is(handlerErr, context.DeadlineExceeded)
	if deadlineExceeded {
		att.timedOut = true
	}

	q.finish(context.Background(), job, def, owner, job.fenceGeneration, handlerErr, att)
}

type panicError struct{ err error }

func (e panicError) Error() string { return e.err.Error() }
func (e panicError) Unwrap() error { return e.err }

func (q *Queue) startHeartbeat(att *runningAttempt) func() {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(q.opts.Heartbeat)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				ctx := context.Background()
				err := q.db.Tx(ctx, func(tx storage.Tx) error {
					res, err := tx.Exec(ctx,
						`UPDATE core_jobs SET lease_expires_at = ?
						  WHERE id = ? AND lease_owner = ? AND fence_generation = ?
						    AND state IN ('running', 'cancel_requested')`,
						rfc(q.now().Add(q.opts.LeaseTTL)),
						att.jobID, att.owner, att.fence)
					if err != nil {
						return err
					}
					n, _ := res.RowsAffected()
					if n != 1 {
						return ErrLostLease
					}
					return nil
				})
				if err != nil {
					att.cancel(ErrLostLease)
					return
				}
			}
		}
	}()
	return func() { close(done) }
}

func (q *Queue) finish(ctx context.Context, job jobRow, def Def, owner string, fence int64, handlerErr error, att *runningAttempt) {
	current, err := q.readJob(ctx, job.id)
	if err != nil {
		slog.Error("jobs: finish read", "job", job.id, "err", err)
		return
	}
	if current.state == StateCancelRequested || (current.state == StateRunning && current.cancelReason != "") {
		reason := current.cancelReason
		if reason == "" {
			reason = ReasonUser
		}
		q.finishCancelled(ctx, job, owner, fence, reason)
		return
	}
	if current.leaseOwner != owner || current.fenceGeneration != fence {
		return // stolen
	}

	if handlerErr == nil {
		q.finishSucceeded(ctx, job, owner, fence)
		return
	}

	if att.timedOut || errors.Is(handlerErr, context.DeadlineExceeded) {
		err := handlerErr
		if err == nil {
			err = context.DeadlineExceeded
		}
		_ = q.failAttempt(ctx, job, owner, fence, fmt.Errorf("timeout: %w", err))
		return
	}
	if errors.Is(handlerErr, context.Canceled) || errors.Is(handlerErr, ErrLostLease) {
		// Recheck: disable/user cancel may have landed; otherwise treat as retryable.
		if current, err := q.readJob(ctx, job.id); err == nil && current.state == StateCancelRequested {
			q.finishCancelled(ctx, job, owner, fence, current.cancelReason)
			return
		}
		if errors.Is(handlerErr, ErrLostLease) {
			return
		}
	}
	q.failAttempt(ctx, job, owner, fence, handlerErr)
}

func (q *Queue) finishSucceeded(ctx context.Context, job jobRow, owner string, fence int64) {
	started, _ := parseTime(ptrStr(job.startedAt))
	dur := q.now().Sub(started)
	err := q.db.Tx(ctx, func(tx storage.Tx) error {
		res, err := tx.Exec(ctx,
			`UPDATE core_jobs
			    SET state = ?, finished_at = ?, progress = 1,
			        lease_owner = '', lease_expires_at = NULL
			  WHERE id = ? AND lease_owner = ? AND fence_generation = ?
			    AND state IN ('running', 'cancel_requested')`,
			string(StateSucceeded), rfc(q.now()), job.id, owner, fence)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return ErrLostLease
		}
		job.state = StateSucceeded
		return q.publishJob(ctx, tx, events.TypeJobSucceeded, job, map[string]any{
			"attempt": job.attempt, "duration": dur.String(),
		})
	})
	if err != nil && !errors.Is(err, ErrLostLease) {
		slog.Error("jobs: succeed", "job", job.id, "err", err)
	}
}

func (q *Queue) finishCancelled(ctx context.Context, job jobRow, owner string, fence int64, reason string) {
	if reason == "" {
		reason = ReasonUser
	}
	err := q.db.Tx(ctx, func(tx storage.Tx) error {
		return q.fenceCancelOwned(ctx, tx, job, owner, fence, reason)
	})
	if err != nil && !errors.Is(err, ErrLostLease) {
		slog.Error("jobs: cancel", "job", job.id, "err", err)
	}
}

func (q *Queue) fenceCancel(ctx context.Context, tx storage.Tx, job jobRow, reason string) error {
	now := rfc(q.now())
	if _, err := tx.Exec(ctx,
		`UPDATE core_jobs
		    SET state = ?, cancel_reason = ?, finished_at = ?,
		        lease_owner = '', lease_expires_at = NULL
		  WHERE id = ?`,
		string(StateCancelled), reason, now, job.id); err != nil {
		return err
	}
	job.state = StateCancelled
	job.cancelReason = reason
	return q.publishJob(ctx, tx, events.TypeJobCancelled, job, map[string]any{
		"attempt": job.attempt, "reason": reason,
	})
}

func (q *Queue) fenceCancelOwned(ctx context.Context, tx storage.Tx, job jobRow, owner string, fence int64, reason string) error {
	res, err := tx.Exec(ctx,
		`UPDATE core_jobs
		    SET state = ?, cancel_reason = ?, finished_at = ?,
		        lease_owner = '', lease_expires_at = NULL
		  WHERE id = ? AND lease_owner = ? AND fence_generation = ?
		    AND state IN ('running', 'cancel_requested')`,
		string(StateCancelled), reason, rfc(q.now()), job.id, owner, fence)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrLostLease
	}
	job.state = StateCancelled
	job.cancelReason = reason
	return q.publishJob(ctx, tx, events.TypeJobCancelled, job, map[string]any{
		"attempt": job.attempt, "reason": reason,
	})
}

func (q *Queue) fenceDead(ctx context.Context, tx storage.Tx, job jobRow, lastErr, class string) error {
	if _, err := tx.Exec(ctx,
		`UPDATE core_jobs
		    SET state = ?, last_error = ?, last_error_class = ?, finished_at = ?,
		        lease_owner = '', lease_expires_at = NULL
		  WHERE id = ?`,
		string(StateDead), lastErr, class, rfc(q.now()), job.id); err != nil {
		return err
	}
	job.state = StateDead
	if err := q.publishJob(ctx, tx, events.TypeJobFailed, job, map[string]any{
		"attempt": job.attempt, "errorClass": class,
	}); err != nil {
		return err
	}
	return q.publishJob(ctx, tx, events.TypeJobDead, job, map[string]any{
		"attempts": job.attempt, "lastError": lastErr,
	})
}

func (q *Queue) failAttempt(ctx context.Context, job jobRow, owner string, fence int64, handlerErr error) error {
	class := ClassError
	msg := handlerErr.Error()
	permanent := isPermanent(handlerErr)
	switch {
	case errors.As(handlerErr, &panicError{}):
		class = ClassPanic
	case errors.Is(handlerErr, context.DeadlineExceeded):
		class = ClassTimeout
	case permanent:
		class = ClassPermanent
	}

	def, _ := q.lookupDef(job.pluginID, job.name)
	retry := !permanent && job.attempt < job.maxAttempts
	delay := def.Backoff.delay(job.attempt)
	if d, ok := retryAfterDelay(handlerErr); ok {
		delay = d
		retry = job.attempt < job.maxAttempts
	}

	return q.db.Tx(ctx, func(tx storage.Tx) error {
		now := q.now()
		if retry {
			next := rfc(now.Add(delay))
			res, err := tx.Exec(ctx,
				`UPDATE core_jobs
				    SET state = ?, next_retry_at = ?, last_error = ?, last_error_class = ?,
				        lease_owner = '', lease_expires_at = NULL
				  WHERE id = ? AND lease_owner = ? AND fence_generation = ?
				    AND state IN ('running', 'cancel_requested')`,
				string(StateRetryWait), next, msg, class, job.id, owner, fence)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			if n != 1 {
				return ErrLostLease
			}
			job.state = StateRetryWait
			if err := q.publishJob(ctx, tx, events.TypeJobFailed, job, map[string]any{
				"attempt": job.attempt, "errorClass": class,
			}); err != nil {
				return err
			}
			q.signal()
			return nil
		}

		state := StateFailed
		if !permanent {
			state = StateDead
		}
		res, err := tx.Exec(ctx,
			`UPDATE core_jobs
			    SET state = ?, last_error = ?, last_error_class = ?, finished_at = ?,
			        lease_owner = '', lease_expires_at = NULL
			  WHERE id = ? AND lease_owner = ? AND fence_generation = ?
			    AND state IN ('running', 'cancel_requested')`,
			string(state), msg, class, rfc(now), job.id, owner, fence)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return ErrLostLease
		}
		job.state = state
		if err := q.publishJob(ctx, tx, events.TypeJobFailed, job, map[string]any{
			"attempt": job.attempt, "errorClass": class,
		}); err != nil {
			return err
		}
		if state == StateDead {
			return q.publishJob(ctx, tx, events.TypeJobDead, job, map[string]any{
				"attempts": job.attempt, "lastError": msg,
			})
		}
		return nil
	})
}

func (q *Queue) cancelAttempt(id int64, reason string) {
	q.attMu.Lock()
	att := q.attempts[id]
	q.attMu.Unlock()
	if att != nil {
		att.cancel(fmt.Errorf("jobs: cancelled: %s", reason))
	}
}

func (q *Queue) cancelPlugin(pluginID, reason string) {
	q.attMu.Lock()
	var ids []int64
	for id, att := range q.attempts {
		if att.pluginID == pluginID {
			ids = append(ids, id)
		}
	}
	q.attMu.Unlock()
	for _, id := range ids {
		_ = q.cancel(context.Background(), id, reason, "")
	}
	// Pending jobs are cancelled at the next claim via CheckWorkTx.
	q.signal()
}

func (q *Queue) cancelAll(reason string) {
	q.attMu.Lock()
	ids := make([]int64, 0, len(q.attempts))
	for id := range q.attempts {
		ids = append(ids, id)
	}
	q.attMu.Unlock()
	for _, id := range ids {
		q.cancelAttempt(id, reason)
	}
}

func ptrStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ---- handler context ----

type jobCtx struct {
	context.Context
	q       *Queue
	job     jobRow
	owner   string
	fence   int64
	attempt int

	mu           sync.Mutex
	buf          []string
	lastProgress time.Time
	lastLogFlush time.Time
}

func (c *jobCtx) JobID() int64 { return c.job.id }
func (c *jobCtx) Attempt() int { return c.attempt }

func (c *jobCtx) Args(into any) error {
	if err := json.Unmarshal([]byte(c.job.args), into); err != nil {
		return Permanent(err)
	}
	return nil
}

func (c *jobCtx) Progress(fraction float64, message string) error {
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	now := c.q.now()
	emit := false
	c.mu.Lock()
	if now.Sub(c.lastProgress) >= c.q.opts.ProgressMinGap {
		c.lastProgress = now
		emit = true
	}
	c.mu.Unlock()

	return c.q.db.Tx(c, func(tx storage.Tx) error {
		res, err := tx.Exec(c,
			`UPDATE core_jobs SET progress = ?, progress_message = ?
			  WHERE id = ? AND lease_owner = ? AND fence_generation = ?
			    AND state IN ('running', 'cancel_requested')`,
			fraction, message, c.job.id, c.owner, c.fence)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return ErrLostLease
		}
		if !emit {
			return nil
		}
		c.job.progress = fraction
		c.job.progressMessage = message
		return c.q.publishJob(c, tx, events.TypeJobProgressed, c.job, map[string]any{
			"fraction": fraction, "message": message,
		})
	})
}

func (c *jobCtx) Logf(format string, args ...any) error {
	line := fmt.Sprintf(format, args...)
	c.mu.Lock()
	c.buf = append(c.buf, line)
	flush := len(c.buf) >= 32 || c.q.now().Sub(c.lastLogFlush) >= 200*time.Millisecond
	c.mu.Unlock()
	if flush {
		return c.flushLogs(c)
	}
	return nil
}

func (c *jobCtx) flushLogs(ctx context.Context) error {
	c.mu.Lock()
	lines := c.buf
	c.buf = nil
	c.lastLogFlush = c.q.now()
	c.mu.Unlock()
	if len(lines) == 0 {
		return nil
	}
	var through int64
	err := c.q.db.Tx(ctx, func(tx storage.Tx) error {
		var owner string
		var fence int64
		var state string
		err := tx.QueryRow(ctx,
			`SELECT lease_owner, fence_generation, state FROM core_jobs WHERE id = ?`,
			c.job.id).Scan(&owner, &fence, &state)
		if err != nil {
			return err
		}
		if owner != c.owner || fence != c.fence {
			return ErrLostLease
		}
		if state != string(StateRunning) && state != string(StateCancelRequested) {
			return ErrLostLease
		}
		for _, line := range lines {
			res, err := tx.Exec(ctx,
				`INSERT INTO core_job_logs(job_id, attempt, fence_generation, at, line)
				 VALUES (?, ?, ?, ?, ?)`,
				c.job.id, c.attempt, c.fence, rfc(c.q.now()), line)
			if err != nil {
				return err
			}
			through, err = res.LastInsertId()
			if err != nil {
				return err
			}
		}
		return c.q.publishJob(ctx, tx, events.TypeJobLogged, c.job, map[string]any{
			"throughLogId": through,
		})
	})
	return err
}
