package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/storage"
)

// Jobs are durable so a crash cannot lose work, which means a finished job is a row
// nobody will read again -- and every enqueue adds one, forever. Events already solve
// this with an age-bounded sweep; jobs need the same, or a busy install grows a table
// it can only ever append to.
//
// Only terminal jobs are eligible. A pending, running, retrying, or cancel-requested
// job is still work the queue owes someone, whatever its age.

// RetentionReport describes one retention pass.
type RetentionReport struct {
	// Jobs is how many finished jobs were removed.
	Jobs int64 `json:"jobs"`
	// Logs is how many of their log lines went with them.
	Logs int64 `json:"logs"`
}

// RunRetention deletes finished jobs older than the age limit, and their logs. A job
// still in flight is never eligible, whatever its age.
func (q *Queue) RunRetention(ctx context.Context) (RetentionReport, error) {
	var rep RetentionReport
	cutoff := q.opts.Now().UTC().Add(-q.opts.Retention).Format(time.RFC3339Nano)

	err := q.db.Tx(ctx, func(tx storage.Tx) error {
		// A terminal job's finished_at is when it stopped mattering. Fall back to
		// created_at so a row written before finished_at existed cannot outlive the
		// sweep forever.
		res, err := tx.Exec(ctx, `DELETE FROM core_job_logs WHERE job_id IN (
			SELECT id FROM core_jobs
			WHERE state IN ('succeeded', 'failed', 'dead', 'cancelled')
			AND coalesce(nullif(finished_at, ''), created_at) < ?)`, cutoff)
		if err != nil {
			return err
		}
		rep.Logs, _ = res.RowsAffected()

		res, err = tx.Exec(ctx, `DELETE FROM core_jobs
			WHERE state IN ('succeeded', 'failed', 'dead', 'cancelled')
			AND coalesce(nullif(finished_at, ''), created_at) < ?`, cutoff)
		if err != nil {
			return err
		}
		rep.Jobs, _ = res.RowsAffected()
		return nil
	})
	if err != nil {
		return RetentionReport{}, err
	}
	if rep.Jobs > 0 {
		slog.Debug("jobs: retention pass", "jobs", rep.Jobs, "logs", rep.Logs)
	}
	return rep, nil
}

// RunRetentionDaily runs a retention pass at startup and once a day until ctx is
// cancelled, matching how the event log is swept.
func (q *Queue) RunRetentionDaily(ctx context.Context) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		if _, err := q.RunRetention(ctx); err != nil && ctx.Err() == nil {
			slog.Error("jobs: retention pass failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
