package harness

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

func (s *Service) insertStarting(ctx context.Context, p Profile, title, workspace string, at time.Time) (int64, error) {
	var id int64
	err := s.db.Tx(ctx, func(tx storage.Tx) error {
		result, err := tx.Exec(ctx, `
INSERT INTO core_harness_sessions
    (profile_id, profile_name, title, workspace, state, created_at)
VALUES (?, ?, ?, ?, 'starting', ?)`, p.ID, p.Name, title, workspace, at.Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
		id, err = result.LastInsertId()
		if err != nil {
			return err
		}
		return s.publishTx(ctx, tx, events.TypeHarnessCreated, id, map[string]any{
			"sessionId": id, "profileId": p.ID, "state": StateStarting,
		})
	})
	return id, err
}

func (s *Service) markStarted(ctx context.Context, id int64, at time.Time) error {
	return s.db.Tx(ctx, func(tx storage.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE core_harness_sessions SET state='running', started_at=? WHERE id=?`, at.Format(time.RFC3339Nano), id); err != nil {
			return err
		}
		return s.publishTx(ctx, tx, events.TypeHarnessStarted, id, map[string]any{"sessionId": id, "state": StateRunning})
	})
}

func (s *Service) markStopping(ctx context.Context, id int64, reason string) error {
	return s.db.Tx(ctx, func(tx storage.Tx) error {
		result, err := tx.Exec(ctx, `UPDATE core_harness_sessions SET state='stopping', stop_reason=? WHERE id=? AND state='running'`, reason, id)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed == 0 {
			return ErrTerminal
		}
		return s.publishTx(ctx, tx, events.TypeHarnessStopping, id, map[string]any{
			"sessionId": id, "state": StateStopping, "reason": reason,
		})
	})
}

func (s *Service) finish(ctx context.Context, id int64, state State, exitCode *int, errText, reason string) error {
	at := s.opts.Now().UTC().Format(time.RFC3339Nano)
	return s.db.Tx(ctx, func(tx storage.Tx) error {
		if _, err := tx.Exec(ctx, `
UPDATE core_harness_sessions
SET state=?, exit_code=?, error=?, stop_reason=?, finished_at=?
WHERE id=?`, state, exitCode, errText, reason, at, id); err != nil {
			return err
		}
		typ := events.TypeHarnessExited
		switch state {
		case StateFailed:
			typ = events.TypeHarnessFailed
		case StateStopped:
			typ = events.TypeHarnessStopped
		case StateInterrupted:
			typ = events.TypeHarnessInterrupted
		}
		return s.publishTx(ctx, tx, typ, id, map[string]any{
			"sessionId": id, "state": state, "exitCode": exitCode, "reason": reason,
		})
	})
}

func (s *Service) appendOutput(ctx context.Context, id int64, stream, text string) error {
	if text == "" {
		return nil
	}
	return s.db.Tx(ctx, func(tx storage.Tx) error {
		if _, err := tx.Exec(ctx, `
INSERT INTO core_harness_output (session_id, stream, text, bytes, created_at)
VALUES (?, ?, ?, ?, ?)`, id, stream, text, len(text), s.opts.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
DELETE FROM core_harness_output
WHERE id IN (
    SELECT id FROM (
        SELECT id, SUM(bytes) OVER (ORDER BY id DESC) AS retained
        FROM core_harness_output
        WHERE session_id = ?
    ) WHERE retained > ?
)`, id, s.opts.MaxOutputBytes)
		return err
	})
}

func (s *Service) List(ctx context.Context, limit int) ([]Session, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.db.Query(ctx, sessionSelect+` ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Session, 0)
	for rows.Next() {
		item, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Service) Get(ctx context.Context, id int64) (*Session, error) {
	item, err := scanSession(s.db.QueryRow(ctx, sessionSelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `
SELECT id, stream, text, created_at FROM core_harness_output
WHERE session_id=? ORDER BY id DESC LIMIT 2001`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var chunk OutputChunk
		var created string
		if err := rows.Scan(&chunk.ID, &chunk.Stream, &chunk.Text, &created); err != nil {
			return nil, err
		}
		chunk.CreatedAt, err = parseTime(created)
		if err != nil {
			return nil, err
		}
		item.Output = append(item.Output, chunk)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(item.Output) > 2000 {
		item.Output = item.Output[:2000]
		item.OutputTruncated = true
	}
	sort.Slice(item.Output, func(i, j int) bool { return item.Output[i].ID < item.Output[j].ID })
	return &item, nil
}

const sessionSelect = `
SELECT id, profile_id, profile_name, title, workspace, state, exit_code,
       error, stop_reason, created_at, started_at, finished_at
FROM core_harness_sessions`

type rowScanner interface{ Scan(...any) error }

func scanSession(row rowScanner) (Session, error) {
	var item Session
	var state, created string
	var exitCode sql.NullInt64
	var started, finished sql.NullString
	if err := row.Scan(
		&item.ID, &item.ProfileID, &item.ProfileName, &item.Title, &item.Workspace,
		&state, &exitCode, &item.Error, &item.StopReason, &created, &started, &finished,
	); err != nil {
		return item, err
	}
	item.State = State(state)
	if exitCode.Valid {
		code := int(exitCode.Int64)
		item.ExitCode = &code
	}
	var err error
	item.CreatedAt, err = parseTime(created)
	if err != nil {
		return item, err
	}
	if started.Valid {
		at, err := parseTime(started.String)
		if err != nil {
			return item, err
		}
		item.StartedAt = &at
	}
	if finished.Valid {
		at, err := parseTime(finished.String)
		if err != nil {
			return item, err
		}
		item.FinishedAt = &at
	}
	return item, nil
}

func parseTime(raw string) (time.Time, error) { return time.Parse(time.RFC3339Nano, raw) }
