package tid

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/hbaldwin98/control-center/host"
)

// localZone is the billing timezone TID reports days in.
func localZone() *time.Location {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		return time.UTC
	}
	return loc
}

func (p *Plugin) summary(ctx context.Context, h host.Host, cfg settings, now time.Time) (summaryPage, error) {
	var err error
	loc := localZone()
	today := now.In(loc)
	monthStart := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, loc).Format("2006-01-02")
	nextMonth := time.Date(today.Year(), today.Month()+1, 1, 0, 0, 0, 0, loc).Format("2006-01-02")
	lastMonthStart := time.Date(today.Year(), today.Month()-1, 1, 0, 0, 0, 0, loc).Format("2006-01-02")
	lastYearStart := time.Date(today.Year()-1, today.Month(), 1, 0, 0, 0, 0, loc).Format("2006-01-02")
	lastYearEnd := time.Date(today.Year()-1, today.Month()+1, 1, 0, 0, 0, 0, loc).Format("2006-01-02")
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
	out.LastBillingPeriodFrom, out.LastBillingPeriodTo, out.LastBillingPeriodKWh, err = latestBillingPeriodTotal(ctx, h)
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

func latestBillingPeriodTotal(ctx context.Context, h host.Host) (string, string, float64, error) {
	var from, to string
	var total sql.NullFloat64
	err := h.Store().QueryRow(ctx, `
		SELECT p.period_start, p.period_end, SUM(r.kwh)
		  FROM tid_billing_periods p
		  LEFT JOIN tid_period_readings r
		    ON r.period_start = p.period_start AND r.period_end = p.period_end
		 GROUP BY p.period_start, p.period_end
		 ORDER BY p.period_start DESC LIMIT 1`).Scan(&from, &to, &total)
	if isNoRows(err) {
		return "", "", 0, nil
	}
	if err != nil {
		return "", "", 0, err
	}
	return parseDay(from), parseDay(to), total.Float64, nil
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
		SELECT day, kwh, cost_cents, on_peak_kwh, off_peak_kwh FROM tid_readings
		 WHERE day >= ? AND day <= ?
		 ORDER BY day DESC`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []dayView{}
	for rows.Next() {
		var d dayView
		if err := rows.Scan(&d.Day, &d.KWh, &d.CostCents, &d.OnPeakKWh, &d.OffPeakKWh); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (p *Plugin) history(ctx context.Context, h host.Host, limit int) (historyPage, error) {
	if limit < 1 || limit > 24 {
		limit = 24
	}
	rows, err := h.Store().Query(ctx, `
		SELECT period_start, period_end, peak_demand_date, peak_demand_kw
		  FROM tid_billing_periods ORDER BY period_start DESC LIMIT ?`, limit)
	if err != nil {
		return historyPage{}, err
	}
	type periodRow struct {
		start, end string
		peakDate   sql.NullString
		peakKW     *float64
	}
	var stored []periodRow
	for rows.Next() {
		var period periodRow
		if err := rows.Scan(&period.start, &period.end, &period.peakDate, &period.peakKW); err != nil {
			rows.Close()
			return historyPage{}, err
		}
		stored = append(stored, period)
	}
	if err := rows.Close(); err != nil {
		return historyPage{}, err
	}
	out := historyPage{Periods: []billingPeriodView{}}
	for _, storedPeriod := range stored {
		period := billingPeriodView{
			Start: parseDay(storedPeriod.start), End: parseDay(storedPeriod.end),
			PeakDemandDate: storedPeriod.peakDate.String, PeakDemandKW: storedPeriod.peakKW,
			Days: []dayView{},
		}
		dayRows, err := h.Store().Query(ctx, `
			SELECT day, kwh, cost_cents, on_peak_kwh, off_peak_kwh
			  FROM tid_period_readings
			 WHERE period_start = ? AND period_end = ? ORDER BY day DESC`, storedPeriod.start, storedPeriod.end)
		if err != nil {
			return historyPage{}, err
		}
		var totalCost int64
		var hasCost bool
		var onPeak, offPeak float64
		var hasOnPeak, hasOffPeak bool
		for dayRows.Next() {
			var day dayView
			if err := dayRows.Scan(&day.Day, &day.KWh, &day.CostCents, &day.OnPeakKWh, &day.OffPeakKWh); err != nil {
				dayRows.Close()
				return historyPage{}, err
			}
			period.TotalKWh += day.KWh
			if day.CostCents != nil {
				totalCost += *day.CostCents
				hasCost = true
			}
			if day.OnPeakKWh != nil {
				onPeak += *day.OnPeakKWh
				hasOnPeak = true
			}
			if day.OffPeakKWh != nil {
				offPeak += *day.OffPeakKWh
				hasOffPeak = true
			}
			period.Days = append(period.Days, day)
		}
		if err := dayRows.Close(); err != nil {
			return historyPage{}, err
		}
		if hasCost {
			period.TotalCostCents = &totalCost
		}
		if hasOnPeak {
			period.OnPeakKWh = &onPeak
		}
		if hasOffPeak {
			period.OffPeakKWh = &offPeak
		}
		out.Periods = append(out.Periods, period)
	}
	lastSync, err := latestSync(ctx, h)
	if err != nil {
		return historyPage{}, err
	}
	out.LatestEvent = latestEventID(nil, lastSync)
	return out, nil
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
