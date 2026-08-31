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
	Source string `json:"source"`
	CSV    string `json:"csv"`
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

	var readings []Reading
	var err error
	_ = jc.Logf("sync starting from source %q", source)
	if source == "upload" {
		if err := jc.Progress(0.2, "parsing upload"); err != nil {
			return err
		}
		readings = Parse([]byte(args.CSV), "text/csv")
		if len(readings) == 0 {
			_ = jc.Logf("upload did not parse as CSV (%d bytes); retrying as JSON", len(args.CSV))
			readings = Parse([]byte(args.CSV), "application/json")
		}
		if len(readings) == 0 {
			_ = jc.Logf("upload parsed as neither CSV nor JSON (%d bytes)", len(args.CSV))
			return hostjobs.Permanent(fmt.Errorf("tid: upload contained no daily readings"))
		}
		_ = jc.Logf("parsed %d readings from the upload", len(readings))
	} else {
		readings, err = p.collect(jc, h, cfg)
		if err != nil {
			_ = jc.Logf("portal collection failed: %v", err)
			_ = p.recordSync(jc, h, "failed", 0, source, err.Error())
			return err
		}
	}

	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	n, err := p.upsertReadings(jc, h, readings, source, now)
	if err != nil {
		_ = jc.Logf("storing %d readings failed: %v", len(readings), err)
		_ = p.recordSync(jc, h, "failed", 0, source, err.Error())
		return err
	}
	_ = jc.Logf("stored %d of %d readings from %s", n, len(readings), source)
	if err := jc.Progress(0.8, "recorded readings"); err != nil {
		return err
	}

	if err := p.recordSync(jc, h, "ok", n, source, ""); err != nil {
		return err
	}
	if err := h.Events().Publish(jc, "synced", now, synced{At: now, Rows: n, Source: source}); err != nil {
		return err
	}

	if err := p.writeInsight(jc, h, cfg); err != nil {
		// The sync itself succeeded; a failed insight is reported, not fatal.
		_ = jc.Logf("insight generation failed (readings are saved): %v", err)
	}
	return nil
}

func (p *Plugin) upsertReadings(ctx hostjobs.Context, h host.Host, readings []Reading, source, at string) (int, error) {
	n := 0
	err := h.Store().Tx(ctx, func(tx hoststorage.Tx) error {
		for _, r := range readings {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var cost any
			if r.CostCents != nil {
				cost = *r.CostCents
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO tid_readings(day, kwh, cost_cents, source, collected_at)
				VALUES (?, ?, ?, ?, ?)
				ON CONFLICT(day) DO UPDATE SET
					kwh = excluded.kwh,
					cost_cents = COALESCE(excluded.cost_cents, tid_readings.cost_cents),
					source = excluded.source,
					collected_at = excluded.collected_at`,
				r.Day, r.KWh, cost, source, at); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
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
