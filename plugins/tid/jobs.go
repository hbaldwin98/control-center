package tid

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hostevents "github.com/hbaldwin98/control-center/host/events"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
	hoststorage "github.com/hbaldwin98/control-center/host/storage"
)

const syncTimeout = 10 * time.Minute

type syncArgs struct {
	Source  string `json:"source"`
	History bool   `json:"history"`
}

type synced struct {
	At     string  `json:"at"`
	Rows   int     `json:"rows"`
	Source string  `json:"source"`
	Day    string  `json:"day,omitempty"`
	KWh    float64 `json:"kwh,omitempty"`
	Body   string  `json:"body"`

	// Named days so a notification rule can pick the one it wants. TID does not
	// fill in a day until the day after it ends, so "latest" is usually an empty
	// or partial today; "settled" is the newest day that actually has usage on
	// it, and is what the default body reports.
	Latest    *dayPayload  `json:"latest,omitempty"`
	Settled   *dayPayload  `json:"settled,omitempty"`
	Today     *dayPayload  `json:"today,omitempty"`
	Yesterday *dayPayload  `json:"yesterday,omitempty"`
	Recent    []dayPayload `json:"recent"`
}

// dayPayload is one daily reading as published on an event.
type dayPayload struct {
	Day        string   `json:"day"`
	KWh        float64  `json:"kwh"`
	CostCents  *int64   `json:"cost_cents,omitempty"`
	OnPeakKWh  *float64 `json:"on_peak_kwh,omitempty"`
	OffPeakKWh *float64 `json:"off_peak_kwh,omitempty"`
	Body       string   `json:"body"`
}

// recentDays is how many trailing days the synced event carries.
const recentDays = 7

func (p *Plugin) sync(jc hostjobs.Context) error {
	h, ok := p.host()
	if !ok {
		return fmt.Errorf("tid: host is not initialized")
	}
	var args syncArgs
	if err := jc.Args(&args); err != nil {
		return err
	}
	cfg := p.settings()
	source := args.Source
	if source == "" {
		source = "portal"
	}

	var result collection
	_ = jc.Logf("sync starting from source %q", source)
	result, err := p.collect(jc, h, cfg, args.History)
	if err != nil {
		_ = jc.Logf("portal collection failed: %v", err)
		_ = p.recordSync(jc, h, "failed", 0, source, err.Error())
		return err
	}

	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	n, inserted, err := p.upsertReadings(jc, h, result, source, now)
	if err != nil {
		_ = jc.Logf("storing %d readings failed: %v", len(result.Readings), err)
		_ = p.recordSync(jc, h, "failed", 0, source, err.Error())
		return err
	}
	_ = jc.Logf("stored %d of %d readings from %s (%d new days)", n, len(result.Readings), source, inserted)
	if err := jc.Progress(0.8, "recorded readings"); err != nil {
		return err
	}

	if err := p.recordSync(jc, h, "ok", n, source, ""); err != nil {
		return err
	}
	payload := syncedPayload(result.Readings, h.Clock().Now(), now, n, source)
	if err := h.Events().Publish(jc, "synced", now, payload); err != nil {
		return err
	}
	if inserted > 0 {
		body := payload.Body
		if body == "" {
			body = fmt.Sprintf("%d new daily reading%s", inserted, plural(inserted))
		}
		if err := h.Events().Publish(jc, "alert", body, alertPayload(payload, body, inserted)); err != nil {
			return err
		}
	}

	if err := p.writeInsight(jc, h, cfg); err != nil {
		// The sync itself succeeded; a failed insight is reported, not fatal.
		_ = jc.Logf("insight generation failed (readings are saved): %v", err)
	}
	return nil
}

func (p *Plugin) upsertReadings(ctx hostjobs.Context, h host.Host, result collection, source, at string) (int, int, error) {
	n := 0
	inserted := 0
	err := h.Store().Tx(ctx, func(tx hoststorage.Tx) error {
		for _, r := range result.Readings {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var exists int
			if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM tid_readings WHERE day = ?`, r.Day).Scan(&exists); err != nil {
				return err
			}
			var cost any
			if r.CostCents != nil {
				cost = *r.CostCents
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO tid_readings(day, kwh, cost_cents, on_peak_kwh, off_peak_kwh, source, collected_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(day) DO UPDATE SET
					kwh = excluded.kwh,
					cost_cents = COALESCE(excluded.cost_cents, tid_readings.cost_cents),
					on_peak_kwh = COALESCE(excluded.on_peak_kwh, tid_readings.on_peak_kwh),
					off_peak_kwh = COALESCE(excluded.off_peak_kwh, tid_readings.off_peak_kwh),
					source = excluded.source,
					collected_at = excluded.collected_at`,
				r.Day, r.KWh, cost, r.OnPeakKWh, r.OffPeakKWh, source, at); err != nil {
				return err
			}
			n++
			if exists == 0 {
				inserted++
			}
		}
		for _, period := range result.Periods {
			period.Start = parseDay(period.Start)
			period.End = parseDay(period.End)
			if _, err := tx.Exec(ctx, `
				INSERT INTO tid_billing_periods(period_start, period_end, peak_demand_date, peak_demand_kw, collected_at)
				VALUES (?, ?, ?, ?, ?)
				ON CONFLICT(period_start, period_end) DO UPDATE SET
					peak_demand_date = COALESCE(excluded.peak_demand_date, tid_billing_periods.peak_demand_date),
					peak_demand_kw = COALESCE(excluded.peak_demand_kw, tid_billing_periods.peak_demand_kw),
					collected_at = excluded.collected_at`,
				period.Start, period.End, nullableString(period.PeakDemandDate), period.PeakDemandKW, at); err != nil {
				return err
			}
			for _, r := range period.Readings {
				if _, err := tx.Exec(ctx, `
					INSERT INTO tid_period_readings(period_start, period_end, day, kwh, cost_cents, on_peak_kwh, off_peak_kwh)
					VALUES (?, ?, ?, ?, ?, ?, ?)
					ON CONFLICT(period_start, period_end, day) DO UPDATE SET
						kwh = excluded.kwh, cost_cents = excluded.cost_cents,
						on_peak_kwh = excluded.on_peak_kwh, off_peak_kwh = excluded.off_peak_kwh`,
					period.Start, period.End, r.Day, r.KWh, r.CostCents, r.OnPeakKWh, r.OffPeakKWh); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return n, inserted, err
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// syncedPayload expands the collected readings into the days a notification
// rule may want to name.
func syncedPayload(readings []Reading, clock time.Time, at string, rows int, source string) synced {
	payload := synced{At: at, Rows: rows, Source: source, Recent: []dayPayload{}}

	byDay := map[string]Reading{}
	days := make([]string, 0, len(readings))
	for _, r := range readings {
		if r.Day == "" {
			continue
		}
		if _, seen := byDay[r.Day]; !seen {
			days = append(days, r.Day)
		}
		byDay[r.Day] = r
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days)))

	local := clock.In(localZone())
	payload.Today = dayFrom(byDay, local.Format("2006-01-02"))
	payload.Yesterday = dayFrom(byDay, local.AddDate(0, 0, -1).Format("2006-01-02"))
	for i, day := range days {
		reading := byDay[day]
		if i == 0 {
			payload.Latest = readingPayload(reading)
		}
		if payload.Settled == nil && reading.KWh > 0 {
			payload.Settled = readingPayload(reading)
		}
		if i < recentDays {
			payload.Recent = append(payload.Recent, *readingPayload(reading))
		}
	}

	// Day/KWh stay on the newest day for compatibility; the body reports the
	// newest day that has usage, because the newest day usually does not yet.
	if payload.Latest != nil {
		payload.Day = payload.Latest.Day
		payload.KWh = payload.Latest.KWh
	}
	switch {
	case payload.Settled != nil:
		payload.Body = payload.Settled.Body
	case payload.Latest != nil:
		payload.Body = payload.Latest.Body
	default:
		payload.Body = "TID synced, no daily reading yet"
	}
	return payload
}

func dayFrom(byDay map[string]Reading, day string) *dayPayload {
	reading, ok := byDay[day]
	if !ok {
		return nil
	}
	return readingPayload(reading)
}

func readingPayload(r Reading) *dayPayload {
	return &dayPayload{
		Day: r.Day, KWh: r.KWh, CostCents: r.CostCents,
		OnPeakKWh: r.OnPeakKWh, OffPeakKWh: r.OffPeakKWh,
		Body: formatReading(r),
	}
}

// alertPayload mirrors the synced day fields so one rule template works on both.
func alertPayload(s synced, body string, inserted int) map[string]any {
	out := map[string]any{
		"title": "New TID usage", "body": body, "rows": inserted, "source": s.Source,
		"day": s.Day, "kwh": s.KWh, "recent": s.Recent,
	}
	for key, day := range map[string]*dayPayload{
		"latest": s.Latest, "settled": s.Settled, "today": s.Today, "yesterday": s.Yesterday,
	} {
		if day != nil {
			out[key] = day
		}
	}
	return out
}

func formatReading(r Reading) string {
	s := fmt.Sprintf("%s: %.1f kWh", r.Day, r.KWh)
	if r.CostCents != nil {
		s += fmt.Sprintf(" ($%.2f)", float64(*r.CostCents)/100)
	}
	return s
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (p *Plugin) recordSync(ctx hostjobs.Context, h host.Host, status string, rows int, source, errMsg string) error {
	at := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	_, err := h.Store().Exec(ctx, `
		INSERT INTO tid_syncs(at, status, rows, source, error) VALUES (?, ?, ?, ?, ?)`,
		at, status, rows, source, errMsg)
	return err
}

func (p *Plugin) onSynced(ctx context.Context, tx hoststorage.Tx, e hostevents.Event) error {
	_, err := tx.Exec(ctx, `
		UPDATE tid_syncs SET event_id = ?
		 WHERE id = (SELECT id FROM (SELECT MAX(id) AS id FROM tid_syncs) t) AND event_id = 0`, e.ID)
	return err
}

func (p *Plugin) onInsight(ctx context.Context, tx hoststorage.Tx, e hostevents.Event) error {
	_, err := tx.Exec(ctx, `
		UPDATE tid_insights SET event_id = ?
		 WHERE id = (SELECT id FROM (SELECT MAX(id) AS id FROM tid_insights) t) AND event_id = 0`, e.ID)
	return err
}
