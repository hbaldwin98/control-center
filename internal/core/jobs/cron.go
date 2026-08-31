package jobs

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	_ "time/tzdata"

	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/policy"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

func (q *Queue) tickCron(ctx context.Context) {
	q.mu.Lock()
	type sched struct {
		pluginID string
		def      Def
	}
	var items []sched
	for k, def := range q.defs {
		if def.Schedule == "" {
			continue
		}
		pluginID, _ := splitDefKey(k)
		items = append(items, sched{pluginID, def})
	}
	q.mu.Unlock()

	now := q.now()
	for _, it := range items {
		if ctx.Err() != nil {
			return
		}
		if err := q.considerSlot(ctx, it.pluginID, it.def, now); err != nil {
			if ctx.Err() != nil {
				return
			}
		}
	}
}

func splitDefKey(k string) (pluginID, name string) {
	i := strings.IndexByte(k, 0)
	if i < 0 {
		return k, ""
	}
	return k[:i], k[i+1:]
}

func (q *Queue) considerSlot(ctx context.Context, pluginID string, def Def, now time.Time) error {
	loc, err := time.LoadLocation(def.TimeZone)
	if err != nil {
		return err
	}
	cr, err := parseCron(def.Schedule)
	if err != nil {
		return err
	}
	slot, slotKey := scheduleSlot(now, loc)
	if !cr.matches(slot) {
		return nil
	}

	enqueued := false
	err = q.db.Tx(ctx, func(tx storage.Tx) error {
		var last string
		err := tx.QueryRow(ctx,
			`SELECT last_slot FROM core_job_schedules WHERE plugin_id = ? AND job_name = ?`,
			pluginID, def.Name).Scan(&last)
		if err != nil && !storage.IsNoRows(err) {
			return err
		}
		if last == slotKey {
			return nil
		}

		admit := true
		if err := q.gate.CheckWorkTx(ctx, tx, pluginID); err != nil {
			if errors.Is(err, policy.ErrPluginDisabled) || errors.Is(err, policy.ErrUnknownPlugin) {
				admit = false
			} else {
				return err
			}
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO core_job_schedules(plugin_id, job_name, timezone, last_slot, updated_at)
			 VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(plugin_id, job_name) DO UPDATE SET
			     last_slot = excluded.last_slot,
			     timezone = excluded.timezone,
			     updated_at = excluded.updated_at`,
			pluginID, def.Name, def.TimeZone, slotKey, rfc(q.now())); err != nil {
			return err
		}
		if !admit {
			return nil
		}

		_, err = q.enqueueTx(ctx, tx, pluginID, def, []byte("{}"), enqueueOpts{
			idempotencyKey: "cron:" + slotKey,
		})
		if err == nil {
			enqueued = true
		}
		return err
	})
	if err == nil && enqueued {
		q.signal()
	}
	return err
}

func scheduleSlot(now time.Time, loc *time.Location) (time.Time, string) {
	local := now.In(loc)
	slot := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute(), 0, 0, loc)
	// Local wall fields identify the slot, so a repeated autumn hour runs once.
	return slot, slot.Format("2006-01-02T15:04")
}

// CatchUpSchedules records the current minute as considered for every stored schedule
// of pluginID, without enqueueing. Enable uses it so slots that passed while the
// plugin was down are not fired.
func (q *Queue) CatchUpSchedules(ctx context.Context, pluginID string) error {
	return q.db.Tx(ctx, func(tx storage.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT job_name, timezone FROM core_job_schedules WHERE plugin_id = ?`, pluginID)
		if err != nil {
			return err
		}
		type schedRow struct{ name, tz string }
		var list []schedRow
		for rows.Next() {
			var r schedRow
			if err := rows.Scan(&r.name, &r.tz); err != nil {
				rows.Close()
				return err
			}
			list = append(list, r)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		now := q.now()
		stamp := rfc(now)
		for _, r := range list {
			loc, err := time.LoadLocation(r.tz)
			if err != nil {
				return err
			}
			_, slotKey := scheduleSlot(now, loc)
			if _, err := tx.Exec(ctx,
				`UPDATE core_job_schedules SET last_slot = ?, updated_at = ? WHERE plugin_id = ? AND job_name = ?`,
				slotKey, stamp, pluginID, r.name); err != nil {
				return err
			}
		}
		return nil
	})
}

func (q *Queue) enqueueTx(ctx context.Context, tx storage.Tx, pluginID string, def Def, payload []byte, o enqueueOpts) (int64, error) {
	if o.idempotencyKey != "" {
		existing, err := scanJob(tx.QueryRow(ctx,
			jobSelect+` WHERE plugin_id = ? AND name = ? AND idempotency_key = ?
			   AND state IN ('pending','running','retry_wait','cancel_requested')`,
			pluginID, def.Name, o.idempotencyKey))
		if err == nil {
			return existing.id, nil
		}
		if !storage.IsNoRows(err) {
			return 0, err
		}
	}

	runAt := q.now()
	if o.hasRunAt {
		runAt = o.runAt.UTC()
	}
	var key any
	if o.idempotencyKey != "" {
		key = o.idempotencyKey
	}
	res, err := tx.Exec(ctx,
		`INSERT INTO core_jobs(
		     plugin_id, name, args, state, max_attempts, concurrency, priority,
		     idempotency_key, run_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pluginID, def.Name, string(payload), string(StatePending),
		def.MaxAttempts, def.Concurrency, o.priority, key, rfc(runAt), rfc(q.now()))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	job := jobRow{id: id, pluginID: pluginID, name: def.Name}
	if err := q.publishJob(ctx, tx, events.TypeJobEnqueued, job, map[string]any{
		"argsDigest": argsDigest(payload),
	}); err != nil {
		return 0, err
	}
	return id, nil
}

// Five-field cron: minute hour day-of-month month day-of-week. No seconds, names, or aliases.
type cronSched struct {
	min, hour, dom, month, dow cronField
	domStar, dowStar           bool
}

type cronField struct{ bits uint64 }

func (f cronField) has(n int) bool { return f.bits&(1<<n) != 0 }

func parseCron(expr string) (*cronSched, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return nil, fmt.Errorf("want 5 fields, got %d", len(parts))
	}
	min, err := parseField(parts[0], 0, 59)
	if err != nil {
		return nil, fmt.Errorf("minute: %w", err)
	}
	hour, err := parseField(parts[1], 0, 23)
	if err != nil {
		return nil, fmt.Errorf("hour: %w", err)
	}
	dom, err := parseField(parts[2], 1, 31)
	if err != nil {
		return nil, fmt.Errorf("day-of-month: %w", err)
	}
	month, err := parseField(parts[3], 1, 12)
	if err != nil {
		return nil, fmt.Errorf("month: %w", err)
	}
	dow, err := parseField(parts[4], 0, 7)
	if err != nil {
		return nil, fmt.Errorf("day-of-week: %w", err)
	}
	// 7 is Sunday in some dialects; treat it as 0.
	if dow.has(7) {
		dow.bits |= 1 << 0
		dow.bits &^= 1 << 7
	}
	return &cronSched{
		min: min, hour: hour, dom: dom, month: month, dow: dow,
		domStar: parts[2] == "*",
		dowStar: parts[4] == "*",
	}, nil
}

func parseField(s string, min, max int) (cronField, error) {
	var f cronField
	for _, part := range strings.Split(s, ",") {
		if err := parsePart(part, min, max, &f); err != nil {
			return cronField{}, err
		}
	}
	return f, nil
}

func parsePart(s string, min, max int, f *cronField) error {
	step := 1
	rangePart := s
	if i := strings.IndexByte(s, '/'); i >= 0 {
		rangePart = s[:i]
		n, err := strconv.Atoi(s[i+1:])
		if err != nil || n <= 0 {
			return fmt.Errorf("bad step %q", s)
		}
		step = n
	}
	var start, end int
	switch {
	case rangePart == "*":
		start, end = min, max
	case strings.Contains(rangePart, "-"):
		a, b, ok := strings.Cut(rangePart, "-")
		if !ok {
			return fmt.Errorf("bad range %q", s)
		}
		var err error
		start, err = strconv.Atoi(a)
		if err != nil {
			return err
		}
		end, err = strconv.Atoi(b)
		if err != nil {
			return err
		}
	default:
		n, err := strconv.Atoi(rangePart)
		if err != nil {
			return err
		}
		start, end = n, n
	}
	if start < min || end > max || start > end {
		return fmt.Errorf("value %d-%d out of %d-%d", start, end, min, max)
	}
	for n := start; n <= end; n += step {
		f.bits |= 1 << n
	}
	return nil
}

func (c *cronSched) matches(t time.Time) bool {
	if !c.min.has(t.Minute()) || !c.hour.has(t.Hour()) || !c.month.has(int(t.Month())) {
		return false
	}
	dom := c.dom.has(t.Day())
	dow := c.dow.has(int(t.Weekday())) // Sunday = 0
	switch {
	case c.domStar && c.dowStar:
		return true
	case c.domStar:
		return dow
	case c.dowStar:
		return dom
	default:
		// Both restricted: classic cron ORs them.
		return dom || dow
	}
}
