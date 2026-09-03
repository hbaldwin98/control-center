package tid

import (
	"context"
	"fmt"
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
	At     string `json:"at"`
	Rows   int    `json:"rows"`
	Source string `json:"source"`
}

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
	if err := h.Events().Publish(jc, "synced", now, synced{At: now, Rows: n, Source: source}); err != nil {
		return err
	}
	if inserted > 0 {
		body := fmt.Sprintf("%d new daily reading%s", inserted, plural(inserted))
		if err := h.Events().Publish(jc, "alert", body, map[string]any{
			"title": "New TID usage", "body": body, "rows": inserted, "source": source,
		}); err != nil {
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
