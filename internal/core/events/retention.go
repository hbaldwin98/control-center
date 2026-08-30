package events

import (
	"context"
	"log/slog"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/storage"
)

// RetentionReport describes one retention pass.
type RetentionReport struct {
	// Deleted is how many rows were removed.
	Deleted int64 `json:"deleted"`
	// OldestRetainedID is the new resume boundary exposed to SSE.
	OldestRetainedID int64 `json:"oldestRetainedId"`
	// PinnedBy names the durable subscriber holding back cleanup, when one is.
	PinnedBy string `json:"pinnedBy,omitempty"`
	// PinnedRows counts rows past their age limit that a stalled cursor still pins.
	PinnedRows int64 `json:"pinnedRows"`
}

// RunRetention deletes only rows older than both boundaries: the age limit, and every row
// still needed by an active or paused durable cursor. It reports disk pressure when a
// stalled cursor prevents cleanup.
func (l *Log) RunRetention(ctx context.Context) (RetentionReport, error) {
	var rep RetentionReport
	cutoff := l.opts.Now().UTC().Add(-l.opts.Retention).Format(time.RFC3339Nano)

	err := l.db.Tx(ctx, func(tx storage.Tx) error {
		// Every row above the lowest cursor position is still owed to a subscriber.
		pin := int64(-1)
		var pinnedBy string
		rows, err := tx.Query(ctx,
			`SELECT subscriber, last_event_id FROM core_event_cursors`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var name string
			var last int64
			if err := rows.Scan(&name, &last); err != nil {
				rows.Close()
				return err
			}
			if pin < 0 || last < pin {
				pin, pinnedBy = last, name
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		if pin < 0 {
			// No durable subscribers: age is the only boundary.
			pin = int64(1) << 62
			pinnedBy = ""
		}

		// Read the highest ID about to go before deleting it. Once the rows are gone,
		// max(id) can no longer tell us where the surviving history begins — and when the
		// delete empties the table entirely, it reads as zero.
		var deletedThrough int64
		if err := tx.QueryRow(ctx,
			`SELECT coalesce(max(id), 0) FROM core_events WHERE created_at < ? AND id <= ?`,
			cutoff, pin).Scan(&deletedThrough); err != nil {
			return err
		}

		res, err := tx.Exec(ctx,
			`DELETE FROM core_events WHERE created_at < ? AND id <= ?`, cutoff, pin)
		if err != nil {
			return err
		}
		rep.Deleted, _ = res.RowsAffected()

		// Rows past their age limit that the pin still protects are disk pressure.
		if pinnedBy != "" {
			if err := tx.QueryRow(ctx,
				`SELECT count(*) FROM core_events WHERE created_at < ? AND id > ?`, cutoff, pin).
				Scan(&rep.PinnedRows); err != nil {
				return err
			}
			if rep.PinnedRows > 0 {
				rep.PinnedBy = pinnedBy
			}
		}

		// The resume boundary is one past the highest ID this pass removed, and never
		// moves backwards. A client below it gets a reset rather than a partial replay.
		var current int64
		if err := tx.QueryRow(ctx,
			`SELECT oldest_retained_id FROM core_event_retention WHERE id = 1`).
			Scan(&current); err != nil {
			return err
		}
		boundary := max(current, deletedThrough+1)
		rep.OldestRetainedID = boundary

		_, err = tx.Exec(ctx,
			`UPDATE core_event_retention SET oldest_retained_id = ?, last_run_at = ? WHERE id = 1`,
			boundary, l.nowString())
		return err
	})
	if err != nil {
		return RetentionReport{}, err
	}

	if rep.PinnedRows > 0 {
		slog.Warn("events: retention blocked by a stalled durable cursor",
			"subscriber", rep.PinnedBy, "pinnedRows", rep.PinnedRows)
	}
	return rep, nil
}

// RunRetentionDaily runs a retention pass at startup and once a day until ctx is cancelled.
func (l *Log) RunRetentionDaily(ctx context.Context) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		if _, err := l.RunRetention(ctx); err != nil && ctx.Err() == nil {
			slog.Error("events: retention pass failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
