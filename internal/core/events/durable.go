package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/storage"
)

// durableSub is one durable subscriber's dispatch loop. Exactly one handler invocation
// runs at a time for each durable name, and a database-backed lease keeps that true across
// processes.
type durableSub struct {
	log       *Log
	cfg       DurableConfig
	pattern   Pattern
	handler   Handler
	txHandler TxHandler

	cancel context.CancelFunc
	done   chan struct{}
}

func (d *durableSub) Name() string { return d.cfg.Name }

// Close stops delivery and releases the dispatch lease. The persisted cursor survives.
func (d *durableSub) Close() {
	if d.cancel != nil {
		d.cancel()
		<-d.done
	}
	d.log.mu.Lock()
	delete(d.log.durables, d.cfg.Name)
	d.log.mu.Unlock()
	_ = d.releaseLease(context.Background())
}

// SubscribeDurable invokes an ordinary handler without holding a write transaction, then
// advances its cursor in a short transaction.
func (l *Log) SubscribeDurable(cfg DurableConfig, h Handler) (Subscription, error) {
	return l.subscribeDurable(cfg, h, nil)
}

// SubscribeDurableTx opens the cursor transaction before invoking the handler and commits
// the handler's database writes with the cursor advance. Use it when handler state and
// acknowledgement must be crash-consistent.
func (l *Log) SubscribeDurableTx(cfg DurableConfig, h TxHandler) (Subscription, error) {
	return l.subscribeDurable(cfg, nil, h)
}

func (l *Log) subscribeDurable(cfg DurableConfig, h Handler, txh TxHandler) (Subscription, error) {
	if cfg.Name == "" {
		return nil, errors.New("events: durable subscription needs a name")
	}
	if !validSegment(cfg.Name) && !isDottedName(cfg.Name) {
		return nil, fmt.Errorf("events: durable name %q must use the [a-z][a-z0-9_]* segment grammar", cfg.Name)
	}
	p, err := CompilePattern(cfg.Pattern)
	if err != nil {
		return nil, err
	}
	if cfg.Retry == (RetryPolicy{}) {
		cfg.Retry = DefaultRetry
	}
	if err := cfg.Retry.validate(); err != nil {
		return nil, err
	}

	ctx := context.Background()
	if err := l.ensureCursor(ctx, cfg, p); err != nil {
		return nil, err
	}

	l.mu.Lock()
	if _, exists := l.durables[cfg.Name]; exists {
		l.mu.Unlock()
		return nil, fmt.Errorf("events: durable subscriber %q is already running in this process", cfg.Name)
	}
	sub := &durableSub{log: l, cfg: cfg, pattern: p, handler: h, txHandler: txh, done: make(chan struct{})}
	l.durables[cfg.Name] = sub
	l.mu.Unlock()

	runCtx, cancel := context.WithCancel(ctx)
	sub.cancel = cancel
	go sub.run(runCtx)
	return sub, nil
}

// isDottedName allows subscriber names such as "core.notifications.deliver".
func isDottedName(name string) bool {
	for _, s := range splitDots(name) {
		if !validSegment(s) {
			return false
		}
	}
	return true
}

func splitDots(s string) []string {
	out := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// ensureCursor creates the cursor for a new durable name or validates the existing one.
// For a new name, FromNow atomically stores the current log tail, FromBeginning stores
// zero, and AfterEvent stores the supplied event ID. An existing cursor always resumes and
// ignores Start.
func (l *Log) ensureCursor(ctx context.Context, cfg DurableConfig, p Pattern) error {
	return l.db.Tx(ctx, func(tx storage.Tx) error {
		var existing string
		err := tx.QueryRow(ctx,
			`SELECT pattern FROM core_event_cursors WHERE subscriber = ?`, cfg.Name).Scan(&existing)
		if err == nil {
			if existing != p.String() {
				return fmt.Errorf("%w: %q has pattern %q, not %q",
					ErrPatternChanged, cfg.Name, existing, p.String())
			}
			return nil
		}
		if !storage.IsNoRows(err) {
			return err
		}

		start := int64(0)
		switch cfg.Start.Mode {
		case FromBeginning, "":
			start = 0
		case FromNow:
			if start, err = TailTx(ctx, tx); err != nil {
				return err
			}
		case AfterEvent:
			if cfg.Start.EventID < 0 {
				return errors.New("events: AfterEvent needs a non-negative event ID")
			}
			start = cfg.Start.EventID
		default:
			return fmt.Errorf("events: unknown start mode %q", cfg.Start.Mode)
		}

		_, err = tx.Exec(ctx,
			`INSERT INTO core_event_cursors(subscriber, pattern, last_event_id, state, updated_at)
			 VALUES (?, ?, ?, 'active', ?)`,
			cfg.Name, p.String(), start, l.nowString())
		return err
	})
}

// cursor is the persisted dispatch position and failure state.
type cursor struct {
	lastEventID   int64
	state         CursorState
	failedEventID int64
	attempts      int
	retryAt       *time.Time
	lastError     string
}

func (l *Log) readCursor(ctx context.Context, name string) (cursor, error) {
	var c cursor
	var state, lastError string
	var retryAt *string
	err := l.db.QueryRow(ctx,
		`SELECT last_event_id, state, failed_event_id, attempts, retry_at, last_error
		   FROM core_event_cursors WHERE subscriber = ?`, name).
		Scan(&c.lastEventID, &state, &c.failedEventID, &c.attempts, &retryAt, &lastError)
	if storage.IsNoRows(err) {
		return c, ErrUnknownName
	}
	if err != nil {
		return c, err
	}
	c.state = CursorState(state)
	c.lastError = lastError
	if retryAt != nil {
		if t, err := time.Parse(time.RFC3339Nano, *retryAt); err == nil {
			c.retryAt = &t
		}
	}
	return c, nil
}

// run is the dispatch loop for one durable name.
func (d *durableSub) run(ctx context.Context) {
	defer close(d.done)
	l := d.log

	ticker := time.NewTicker(l.opts.PollInterval)
	defer ticker.Stop()

	for {
		if err := ctx.Err(); err != nil {
			return
		}
		idle := d.step(ctx)
		if !idle {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// step performs at most one unit of work. It reports whether the loop should now wait.
func (d *durableSub) step(ctx context.Context) (idle bool) {
	l := d.log

	held, err := d.acquireLease(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("events: lease failed", "sub", d.cfg.Name, "err", err)
		}
		return true
	}
	if !held {
		return true
	}

	c, err := l.readCursor(ctx, d.cfg.Name)
	if err != nil {
		return true
	}
	if c.state == CursorPaused {
		return true
	}
	if c.retryAt != nil && l.opts.Now().UTC().Before(*c.retryAt) {
		return true
	}

	batch, err := l.readAfter(ctx, c.lastEventID, 100)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("events: durable read failed", "sub", d.cfg.Name, "err", err)
		}
		return true
	}
	if len(batch) == 0 {
		return true
	}

	// Nonmatching rows advance the cursor immediately, in one write for the whole run.
	skipTo := int64(0)
	for _, e := range batch {
		if d.pattern.Matches(e.Type) {
			break
		}
		skipTo = e.ID
	}
	if skipTo > 0 {
		if err := d.advance(ctx, skipTo); err != nil {
			slog.Error("events: cursor advance failed", "sub", d.cfg.Name, "err", err)
			return true
		}
		return false
	}

	d.dispatch(ctx, batch[0])
	return false
}

// dispatch delivers one matching event. A matching row advances the cursor only after the
// handler succeeds, which is what makes delivery at-least-once.
func (d *durableSub) dispatch(ctx context.Context, e Event) {
	l := d.log

	var handlerErr error
	if d.txHandler != nil {
		// The handler's writes and the cursor advance commit together.
		handlerErr = l.db.Tx(ctx, func(tx storage.Tx) error {
			if err := d.txHandler(ctx, tx, e); err != nil {
				return err
			}
			return d.advanceTx(ctx, tx, e.ID)
		})
	} else {
		if handlerErr = d.handler(ctx, e); handlerErr == nil {
			handlerErr = d.advance(ctx, e.ID)
		}
	}
	if handlerErr == nil {
		return
	}
	if ctx.Err() != nil {
		return
	}
	if err := d.recordFailure(ctx, e, handlerErr); err != nil {
		slog.Error("events: recording handler failure failed", "sub", d.cfg.Name, "err", err)
	}
}

func (d *durableSub) advance(ctx context.Context, id int64) error {
	return d.log.db.Tx(ctx, func(tx storage.Tx) error { return d.advanceTx(ctx, tx, id) })
}

func (d *durableSub) advanceTx(ctx context.Context, tx storage.Tx, id int64) error {
	_, err := tx.Exec(ctx,
		`UPDATE core_event_cursors
		    SET last_event_id = ?, attempts = 0, retry_at = NULL, last_error = '', updated_at = ?
		  WHERE subscriber = ?`,
		id, d.log.nowString(), d.cfg.Name)
	return err
}

// recordFailure applies the retry policy. After the configured attempt threshold the
// subscription pauses on the poison event and publishes core.event.subscription_paused for
// operational alerting; later events do not pass it silently.
func (d *durableSub) recordFailure(ctx context.Context, e Event, cause error) error {
	l := d.log
	return l.db.Tx(ctx, func(tx storage.Tx) error {
		var attempts int
		if err := tx.QueryRow(ctx,
			`SELECT attempts FROM core_event_cursors WHERE subscriber = ?`, d.cfg.Name).
			Scan(&attempts); err != nil {
			return err
		}
		attempts++

		if attempts >= d.cfg.Retry.MaxAttempts {
			if _, err := tx.Exec(ctx,
				`UPDATE core_event_cursors
				    SET state = 'paused', failed_event_id = ?, attempts = ?, retry_at = NULL,
				        last_error = ?, updated_at = ?
				  WHERE subscriber = ?`,
				e.ID, attempts, truncate(cause.Error(), 500), l.nowString(), d.cfg.Name); err != nil {
				return err
			}
			slog.Error("events: subscription paused on a poison event",
				"sub", d.cfg.Name, "event", e.ID, "attempts", attempts, "err", cause)
			_, err := l.PublishTx(ctx, tx, Input{
				Type:    TypeSubscriptionPaused,
				Source:  SourceEvents,
				Subject: d.cfg.Name,
				Payload: map[string]any{
					"subscriber": d.cfg.Name,
					"eventId":    e.ID,
					"attempts":   attempts,
					"lastError":  truncate(cause.Error(), 500),
				},
			})
			return err
		}

		retryAt := l.opts.Now().UTC().Add(d.cfg.Retry.delay(attempts))
		_, err := tx.Exec(ctx,
			`UPDATE core_event_cursors
			    SET attempts = ?, retry_at = ?, last_error = ?, updated_at = ?
			  WHERE subscriber = ?`,
			attempts, retryAt.Format(time.RFC3339Nano), truncate(cause.Error(), 500),
			l.nowString(), d.cfg.Name)
		return err
	})
}

// ---- leases ----

// acquireLease takes or renews the single dispatch lease for this durable name.
func (d *durableSub) acquireLease(ctx context.Context) (bool, error) {
	l := d.log
	now := l.opts.Now().UTC()
	until := now.Add(l.opts.LeaseTTL)

	var held bool
	err := l.db.Tx(ctx, func(tx storage.Tx) error {
		res, err := tx.Exec(ctx,
			`UPDATE core_event_cursors
			    SET lease_owner = ?, lease_until = ?, updated_at = ?
			  WHERE subscriber = ?
			    AND (lease_owner = '' OR lease_owner = ? OR lease_until IS NULL OR lease_until < ?)`,
			l.opts.Owner, until.Format(time.RFC3339Nano), l.nowString(),
			d.cfg.Name, l.opts.Owner, now.Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		held = n > 0
		return nil
	})
	return held, err
}

func (d *durableSub) releaseLease(ctx context.Context) error {
	_, err := d.log.db.Exec(ctx,
		`UPDATE core_event_cursors SET lease_owner = '', lease_until = NULL, updated_at = ?
		  WHERE subscriber = ? AND lease_owner = ?`,
		d.log.nowString(), d.cfg.Name, d.log.opts.Owner)
	return err
}

// ---- administration ----

// Subscribers reports every persisted durable cursor.
func (l *Log) Subscribers(ctx context.Context) ([]SubscriberStatus, error) {
	rows, err := l.db.Query(ctx,
		`SELECT subscriber, pattern, last_event_id, state, failed_event_id, attempts,
		        retry_at, last_error
		   FROM core_event_cursors ORDER BY subscriber`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SubscriberStatus
	for rows.Next() {
		var s SubscriberStatus
		var state, lastError string
		var retryAt *string
		if err := rows.Scan(&s.Name, &s.Pattern, &s.LastEventID, &state,
			&s.FailedEventID, &s.Attempts, &retryAt, &lastError); err != nil {
			return nil, err
		}
		s.State = CursorState(state)
		s.LastError = lastError
		s.Healthy = s.State == CursorActive
		if retryAt != nil {
			if t, err := time.Parse(time.RFC3339Nano, *retryAt); err == nil {
				s.RetryAt = &t
			}
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// RetryPaused clears the failure state so the dispatcher retries the poison event.
func (l *Log) RetryPaused(ctx context.Context, name string) error {
	return l.mutatePaused(ctx, name, func(ctx context.Context, tx storage.Tx, c cursor) error {
		_, err := tx.Exec(ctx,
			`UPDATE core_event_cursors
			    SET state = 'active', attempts = 0, retry_at = NULL, updated_at = ?
			  WHERE subscriber = ?`, l.nowString(), name)
		return err
	})
}

// SkipPaused advances past the poison event and resumes.
func (l *Log) SkipPaused(ctx context.Context, name string) error {
	return l.mutatePaused(ctx, name, func(ctx context.Context, tx storage.Tx, c cursor) error {
		_, err := tx.Exec(ctx,
			`UPDATE core_event_cursors
			    SET state = 'active', last_event_id = ?, failed_event_id = 0, attempts = 0,
			        retry_at = NULL, last_error = '', updated_at = ?
			  WHERE subscriber = ?`, c.failedEventID, l.nowString(), name)
		return err
	})
}

func (l *Log) mutatePaused(ctx context.Context, name string,
	fn func(context.Context, storage.Tx, cursor) error) error {
	return l.db.Tx(ctx, func(tx storage.Tx) error {
		var state string
		var failed int64
		err := tx.QueryRow(ctx,
			`SELECT state, failed_event_id FROM core_event_cursors WHERE subscriber = ?`, name).
			Scan(&state, &failed)
		if storage.IsNoRows(err) {
			return ErrUnknownName
		}
		if err != nil {
			return err
		}
		if CursorState(state) != CursorPaused {
			return ErrNotPaused
		}
		return fn(ctx, tx, cursor{state: CursorPaused, failedEventID: failed})
	})
}

// ResetCursor moves a durable subscriber to a new position. Resetting is an explicit
// administrative operation; it also releases whatever retention pin the old position held.
func (l *Log) ResetCursor(ctx context.Context, name string, start CursorStart) error {
	return l.db.Tx(ctx, func(tx storage.Tx) error {
		var existing string
		err := tx.QueryRow(ctx,
			`SELECT pattern FROM core_event_cursors WHERE subscriber = ?`, name).Scan(&existing)
		if storage.IsNoRows(err) {
			return ErrUnknownName
		}
		if err != nil {
			return err
		}

		var to int64
		switch start.Mode {
		case FromBeginning, "":
			to = 0
		case FromNow:
			if to, err = TailTx(ctx, tx); err != nil {
				return err
			}
		case AfterEvent:
			to = start.EventID
		default:
			return fmt.Errorf("events: unknown start mode %q", start.Mode)
		}

		_, err = tx.Exec(ctx,
			`UPDATE core_event_cursors
			    SET last_event_id = ?, state = 'active', failed_event_id = 0, attempts = 0,
			        retry_at = NULL, last_error = '', updated_at = ?
			  WHERE subscriber = ?`, to, l.nowString(), name)
		return err
	})
}

// RemoveSubscriber deletes a durable cursor, explicitly releasing its retention pin.
func (l *Log) RemoveSubscriber(ctx context.Context, name string) error {
	l.mu.Lock()
	sub := l.durables[name]
	l.mu.Unlock()
	if sub != nil {
		sub.Close()
	}
	_, err := l.db.Exec(ctx, `DELETE FROM core_event_cursors WHERE subscriber = ?`, name)
	return err
}

func (l *Log) nowString() string {
	return l.opts.Now().UTC().Format(time.RFC3339Nano)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
