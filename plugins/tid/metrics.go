package tid

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/hbaldwin98/control-center/host"
)

func (p *Plugin) summary(ctx context.Context, h host.Host, cfg settings, now time.Time) (summaryPage, error) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		loc = time.UTC
	}
	today := now.In(loc)
	monthStart := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, loc).Format("2006-01-02")
	nextMonth := time.Date(today.Year(), today.Month()+1, 1, 0, 0, 0, 0, loc).Format("2006-01-02")
	lastMonthStart := time.Date(today.Year(), today.Month()-1, 1, 0, 0, 0, 0, loc).Format("2006-01-02")
	lastYearStart := time.Date(today.Year()-1, today.Month(), 1, 0, 0, 0, 0, loc).Format("2006-01-02")
	lastYearEnd := time.Date(today.Year()-1, today.Month()+1, 1, 0, 0, 0, 0, loc).Format("2006-01-02")
	ago7 := today.AddDate(0, 0, -6).Format("2006-01-02")
	ago30 := today.AddDate(0, 0, -29).Format("2006-01-02")
	todayISO := today.Format("2006-01-02")

	out := summaryPage{Days: []dayView{}, Spark: []float64{}}

	out.MonthKWh, err = sumKWh(ctx, h, monthStart, nextMonth)
	if err != nil {
		return out, err
	}
	out.LastMonthKWh, err = sumKWh(ctx, h, lastMonthStart, monthStart)
	if err != nil {
		return out, err
	}
	out.LastYearKWh, err = sumKWh(ctx, h, lastYearStart, lastYearEnd)
	if err != nil {
		return out, err
	}

	days, err := listDays(ctx, h, ago30, todayISO)
	if err != nil {
		return out, err
	}
	out.Days = days
	out.Spark = sparkFrom(days, ago30, todayISO)
	out.Avg7 = avgFrom(days, ago7)
	out.PeakDay, out.PeakKWh = peakFrom(days)
	out.EstCostCents = estimateCost(out.MonthKWh, days, monthStart, nextMonth, cfg.CentsPerKWh)

	out.Insight, err = latestInsight(ctx, h)
	if err != nil {
		return out, err
	}
	out.LastSync, err = latestSync(ctx, h)
	if err != nil {
		return out, err
	}
	out.LatestEvent = latestEventID(out.Insight, out.LastSync)
	return out, nil
}

func sumKWh(ctx context.Context, h host.Host, from, to string) (float64, error) {
	var v sql.NullFloat64
	err := h.Store().QueryRow(ctx,
		`SELECT SUM(kwh) FROM tid_readings WHERE day >= ? AND day < ?`, from, to).Scan(&v)
	if err != nil {
		return 0, err
	}
	if !v.Valid {
		return 0, nil
	}
	return v.Float64, nil
}

func listDays(ctx context.Context, h host.Host, from, to string) ([]dayView, error) {
	rows, err := h.Store().Query(ctx, `
		SELECT day, kwh, cost_cents FROM tid_readings
		 WHERE day >= ? AND day <= ?
		 ORDER BY day DESC`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []dayView{}
	for rows.Next() {
		var d dayView
		if err := rows.Scan(&d.Day, &d.KWh, &d.CostCents); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func sparkFrom(days []dayView, from, to string) []float64 {
	by := map[string]float64{}
	for _, d := range days {
		by[d.Day] = d.KWh
	}
	start, err := time.Parse("2006-01-02", from)
	if err != nil {
		return nil
	}
	end, err := time.Parse("2006-01-02", to)
	if err != nil {
		return nil
	}
	var spark []float64
	for t := start; !t.After(end); t = t.AddDate(0, 0, 1) {
		spark = append(spark, by[t.Format("2006-01-02")])
	}
	return spark
}

func avgFrom(days []dayView, from string) float64 {
	var sum float64
	var n int
	for _, d := range days {
		if d.Day >= from {
			sum += d.KWh
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return math.Round(sum/float64(n)*10) / 10
}

func peakFrom(days []dayView) (string, float64) {
	var day string
	var kwh float64
	for _, d := range days {
		if d.KWh > kwh {
			kwh = d.KWh
			day = d.Day
		}
	}
	return day, kwh
}

func estimateCost(monthKWh float64, days []dayView, from, to string, centsPerKWh float64) *int64 {
	var billed int64
	var have bool
	for _, d := range days {
		if d.Day >= from && d.Day < to && d.CostCents != nil {
			billed += *d.CostCents
			have = true
		}
	}
	if have {
		return &billed
	}
	if centsPerKWh <= 0 || monthKWh <= 0 {
		return nil
	}
	c := int64(monthKWh*centsPerKWh + 0.5)
	return &c
}

func latestInsight(ctx context.Context, h host.Host) (*insightView, error) {
	var v insightView
	var anomalies string
	err := h.Store().QueryRow(ctx, `
		SELECT at, summary, recommendation, anomalies, event_id
		  FROM tid_insights ORDER BY id DESC LIMIT 1`).Scan(
		&v.At, &v.Summary, &v.Recommendation, &anomalies, &v.EventID)
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	_ = json.Unmarshal([]byte(anomalies), &v.Anomalies)
	if v.Anomalies == nil {
		v.Anomalies = []string{}
	}
	return &v, nil
}

func latestSync(ctx context.Context, h host.Host) (*syncView, error) {
	var v syncView
	err := h.Store().QueryRow(ctx, `
		SELECT at, status, rows, source, error, event_id
		  FROM tid_syncs ORDER BY id DESC LIMIT 1`).Scan(
		&v.At, &v.Status, &v.Rows, &v.Source, &v.Error, &v.EventID)
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	return &v, nil
}

func latestEventID(insight *insightView, sync *syncView) int64 {
	var n int64
	if insight != nil && insight.EventID > n {
		n = insight.EventID
	}
	if sync != nil && sync.EventID > n {
		n = sync.EventID
	}
	return n
}

func isNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
