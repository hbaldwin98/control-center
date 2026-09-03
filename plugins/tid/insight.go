package tid

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hostai "github.com/hbaldwin98/control-center/host/ai"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
)

var insightSchema = json.RawMessage(`{
	"type":"object",
	"additionalProperties":false,
	"required":["summary","recommendation","anomalies"],
	"properties":{
		"summary":{"type":"string"},
		"recommendation":{"type":"string"},
		"anomalies":{"type":"array","items":{"type":"string"}}
	}
}`)

type insightPayload struct {
	At             string   `json:"at"`
	Summary        string   `json:"summary"`
	Recommendation string   `json:"recommendation"`
	Anomalies      []string `json:"anomalies"`
	Body           string   `json:"body"`
}

// historyDays is how far back the insight reads. Two years covers a full
// seasonal cycle plus the year-over-year comparison for the current month.
const historyDays = 760

// insightRecentDays is the window the insight is written about; everything
// older is context it gets compared against.
const insightRecentDays = 30

func (p *Plugin) writeInsight(jc hostjobs.Context, h host.Host, cfg settings) error {
	now := h.Clock().Now().In(localZone())
	to := now.Format("2006-01-02")
	recent, err := listDays(jc, h, now.AddDate(0, 0, -(insightRecentDays-1)).Format("2006-01-02"), to)
	if err != nil {
		return err
	}
	if len(recent) < 3 {
		return nil
	}
	history, err := listDays(jc, h, now.AddDate(0, 0, -historyDays).Format("2006-01-02"), to)
	if err != nil {
		return err
	}
	months, err := monthlyStats(jc, h, 24, now)
	if err != nil {
		return err
	}

	prompt := insightPrompt(now, recent, history, months, cfg)
	_ = jc.Logf("asking model for an insight over %d recent days, %d days of history, %d months",
		len(recent), len(history), len(months))
	aiStart := time.Now()
	resp, err := h.AI().Chat(jc, hostai.ChatRequest{
		Model:    "cheap-chat",
		Schema:   insightSchema,
		Messages: []hostai.Message{{Role: hostai.RoleUser, Text: prompt}},
	})
	if err != nil {
		_ = jc.Logf("insight model call failed after %dms: %v", time.Since(aiStart).Milliseconds(), err)
		return err
	}
	_ = jc.Logf("insight model answered in %dms (%d in / %d out tokens)",
		time.Since(aiStart).Milliseconds(), resp.Usage.InputTokens, resp.Usage.OutputTokens)

	var parsed struct {
		Summary        string   `json:"summary"`
		Recommendation string   `json:"recommendation"`
		Anomalies      []string `json:"anomalies"`
	}
	if len(resp.Parsed) > 0 {
		if err := json.Unmarshal(resp.Parsed, &parsed); err != nil {
			_ = jc.Logf("structured insight did not unmarshal, falling back to plain text: %v", err)
		}
	} else {
		_ = jc.Logf("model returned no structured output; falling back to plain text")
	}
	if parsed.Summary == "" {
		parsed.Summary = strings.TrimSpace(resp.Text)
	}
	if parsed.Summary == "" {
		return fmt.Errorf("tid: empty insight")
	}
	if parsed.Anomalies == nil {
		parsed.Anomalies = []string{}
	}
	anomalies, _ := json.Marshal(parsed.Anomalies)
	at := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	if _, err := h.Store().Exec(jc, `
		INSERT INTO tid_insights(at, summary, recommendation, anomalies) VALUES (?, ?, ?, ?)`,
		at, parsed.Summary, parsed.Recommendation, string(anomalies)); err != nil {
		return err
	}
	body := parsed.Summary
	if parsed.Recommendation != "" {
		body = parsed.Summary + "\n" + parsed.Recommendation
	}
	return h.Events().Publish(jc, "insight", at, insightPayload{
		At:             at,
		Summary:        parsed.Summary,
		Recommendation: parsed.Recommendation,
		Anomalies:      parsed.Anomalies,
		Body:           body,
	})
}

// insightPrompt lays out the arithmetic the model should not be doing itself:
// daily detail for the recent window, the seasonal history behind it, and a
// temperature-matched expectation for each recent day.
func insightPrompt(now time.Time, recent, history []dayView, months []monthStat, cfg settings) string {
	var b strings.Builder
	b.WriteString(
		"You are the analyst for one household's electricity account with Turlock Irrigation District " +
			"in Turlock, California. Usage here is dominated by cooling in summer and heating in winter, " +
			"so judge a day against days of similar outdoor temperature, not against a flat average.\n\n" +
			"Write:\n" +
			"- summary: 2-4 sentences on where usage stands versus the previous month, versus the same period " +
			"last year, and whether temperature explains the difference. Use numbers.\n" +
			"- anomalies: days or stretches far from what their temperature predicts, each naming the day, the " +
			"kWh, the high temperature, and how far off the temperature-matched expectation it was. Leave the " +
			"list empty if nothing stands out.\n" +
			"- recommendation: one concrete action for this household, tied to a pattern in the data " +
			"(on-peak share, a recurring weekday, a persistent overnight baseline, a seasonal trend).\n\n" +
			"Rules: use only the data below. Do not invent appliances, occupancy, rates, or weather. Where a " +
			"partial month or a missing temperature limits a comparison, say so plainly.\n\n")

	fmt.Fprintf(&b, "Today is %s.", now.Format("Monday 2006-01-02"))
	if cfg.CentsPerKWh > 0 {
		fmt.Fprintf(&b, " Unbilled days are estimated at %.1f cents/kWh.", cfg.CentsPerKWh)
	}
	b.WriteString("\n\n")

	writeMonthlyHistory(&b, months, now)
	writeTemperatureBands(&b, history)
	writeWindows(&b, history, now)
	writeRecentDays(&b, recent, history)
	return b.String()
}

func writeMonthlyHistory(b *strings.Builder, months []monthStat, now time.Time) {
	if len(months) == 0 {
		return
	}
	thisMonth := now.Format("2006-01")
	b.WriteString("MONTHLY HISTORY (oldest first; kWh, cost when every day is billed, avg daily high, days of data)\n")
	for _, m := range months {
		fmt.Fprintf(b, "%s  %7.1f kWh", m.Month, m.KWh)
		if m.CostCents != nil {
			fmt.Fprintf(b, "  $%7.2f", float64(*m.CostCents)/100)
		} else {
			b.WriteString("         -")
		}
		if m.AvgHighF != nil {
			fmt.Fprintf(b, "  %5.1fF", *m.AvgHighF)
		} else {
			b.WriteString("       -")
		}
		fmt.Fprintf(b, "  %2d days", m.Days)
		if m.Month == thisMonth {
			b.WriteString("  (month in progress)")
		}
		b.WriteByte('\n')
	}
	// The year-over-year pair is the comparison people actually ask about, so
	// state it rather than making the model find and divide both rows.
	byMonth := map[string]monthStat{}
	for _, m := range months {
		byMonth[m.Month] = m
	}
	current, hasCurrent := byMonth[thisMonth]
	lastYear, hasLastYear := byMonth[now.AddDate(-1, 0, 0).Format("2006-01")]
	if hasCurrent && hasLastYear && current.Days > 0 && lastYear.Days > 0 {
		curRate := current.KWh / float64(current.Days)
		prevRate := lastYear.KWh / float64(lastYear.Days)
		fmt.Fprintf(b, "\nSame month last year: %.1f kWh/day over %d days, versus %.1f kWh/day over %d days so far this month (%+.0f%%).\n",
			prevRate, lastYear.Days, curRate, current.Days, percentChange(prevRate, curRate))
	}
	b.WriteByte('\n')
}

// tempBand buckets similar-temperature days from the whole history; its average
// is what a recent day at that temperature gets measured against.
type tempBand struct {
	low, high int
	days      int
	kwh       float64
}

func (t tempBand) avg() float64 { return t.kwh / float64(t.days) }

func (t tempBand) label() string {
	switch {
	case t.low <= -900:
		return fmt.Sprintf("under %dF", t.high)
	case t.high >= 900:
		return fmt.Sprintf("%dF and up", t.low)
	default:
		return fmt.Sprintf("%d-%dF", t.low, t.high-1)
	}
}

var bandEdges = []int{60, 70, 80, 90, 100}

func temperatureBands(history []dayView) []tempBand {
	bands := make([]tempBand, 0, len(bandEdges)+1)
	prev := -1000
	for _, edge := range bandEdges {
		bands = append(bands, tempBand{low: prev, high: edge})
		prev = edge
	}
	bands = append(bands, tempBand{low: prev, high: 1000})
	for _, d := range history {
		temp := dayTemp(d)
		if temp == nil || d.KWh <= 0 {
			continue
		}
		for i := range bands {
			if float64(bands[i].low) <= *temp && *temp < float64(bands[i].high) {
				bands[i].days++
				bands[i].kwh += d.KWh
				break
			}
		}
	}
	out := make([]tempBand, 0, len(bands))
	for _, band := range bands {
		if band.days > 0 {
			out = append(out, band)
		}
	}
	return out
}

func writeTemperatureBands(b *strings.Builder, history []dayView) {
	bands := temperatureBands(history)
	if len(bands) == 0 {
		b.WriteString("No daily temperatures are on record, so usage cannot be temperature-adjusted.\n\n")
		return
	}
	fmt.Fprintf(b, "USAGE BY OUTDOOR HIGH TEMPERATURE (%d days on record carry a temperature)\n", countTemps(history))
	for _, band := range bands {
		fmt.Fprintf(b, "%-12s  %5.1f kWh/day avg over %3d days\n", band.label(), band.avg(), band.days)
	}
	b.WriteByte('\n')
}

func writeWindows(b *strings.Builder, history []dayView, now time.Time) {
	windows := []struct {
		name       string
		start, end time.Time
	}{
		{"last 30 days", now.AddDate(0, 0, -29), now},
		{"the 30 days before that", now.AddDate(0, 0, -59), now.AddDate(0, 0, -30)},
		{"the same 30 days last year", now.AddDate(-1, 0, -29), now.AddDate(-1, 0, 0)},
	}
	b.WriteString("WINDOW COMPARISON\n")
	for _, w := range windows {
		stats := windowStats(history, w.start.Format("2006-01-02"), w.end.Format("2006-01-02"))
		if stats.days == 0 {
			fmt.Fprintf(b, "%-27s no data\n", w.name+":")
			continue
		}
		fmt.Fprintf(b, "%-27s %5.1f kWh/day over %2d days", w.name+":", stats.kwh/float64(stats.days), stats.days)
		if stats.tempDays > 0 {
			fmt.Fprintf(b, ", avg high %.0fF", stats.temp/float64(stats.tempDays))
		}
		if stats.onPeak > 0 && stats.kwh > 0 {
			fmt.Fprintf(b, ", %.0f%% on-peak", 100*stats.onPeak/stats.kwh)
		}
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
}

type window struct {
	days     int
	kwh      float64
	onPeak   float64
	temp     float64
	tempDays int
}

func windowStats(days []dayView, from, to string) window {
	var w window
	for _, d := range days {
		if d.Day < from || d.Day > to {
			continue
		}
		w.days++
		w.kwh += d.KWh
		if d.OnPeakKWh != nil {
			w.onPeak += *d.OnPeakKWh
		}
		if temp := dayTemp(d); temp != nil {
			w.temp += *temp
			w.tempDays++
		}
	}
	return w
}

func writeRecentDays(b *strings.Builder, recent, history []dayView) {
	bands := temperatureBands(history)
	fmt.Fprintf(b, "LAST %d DAYS (oldest first): day, weekday, kWh, cost, on-peak kWh, high/low temperature", len(recent))
	if len(bands) > 0 {
		b.WriteString(", and the expected kWh for that temperature from the table above")
	}
	b.WriteString(".\n")
	ordered := slices.Clone(recent)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Day < ordered[j].Day })
	for _, d := range ordered {
		weekday := "   "
		if t, err := time.Parse("2006-01-02", d.Day); err == nil {
			weekday = t.Format("Mon")
		}
		fmt.Fprintf(b, "%s %s %6.1f kWh", d.Day, weekday, d.KWh)
		if d.CostCents != nil {
			fmt.Fprintf(b, " $%6.2f", float64(*d.CostCents)/100)
		} else {
			b.WriteString("        ")
		}
		if d.OnPeakKWh != nil {
			fmt.Fprintf(b, "  on-peak %5.1f", *d.OnPeakKWh)
		}
		temp := dayTemp(d)
		switch {
		case d.HighTempF != nil && d.LowTempF != nil:
			fmt.Fprintf(b, "  %3.0f/%3.0fF", *d.HighTempF, *d.LowTempF)
		case temp != nil:
			fmt.Fprintf(b, "  %3.0fF", *temp)
		default:
			b.WriteString("  no temp")
		}
		if expected, ok := expectedKWh(bands, temp); ok && d.KWh > 0 {
			fmt.Fprintf(b, "  expected %5.1f (%+.0f%%)", expected, percentChange(expected, d.KWh))
		}
		b.WriteByte('\n')
	}
}

// expectedKWh is the historical average for days in the same temperature band.
// Thin bands are skipped rather than passed off as a baseline.
func expectedKWh(bands []tempBand, temp *float64) (float64, bool) {
	if temp == nil {
		return 0, false
	}
	for _, band := range bands {
		if float64(band.low) <= *temp && *temp < float64(band.high) && band.days >= 3 {
			return band.avg(), true
		}
	}
	return 0, false
}

// dayTemp prefers the daily high, which drives cooling load, and falls back to
// whatever else the payload carried.
func dayTemp(d dayView) *float64 {
	if d.HighTempF != nil {
		return d.HighTempF
	}
	if d.AvgTempF != nil {
		return d.AvgTempF
	}
	return d.LowTempF
}

func countTemps(days []dayView) int {
	n := 0
	for _, d := range days {
		if dayTemp(d) != nil {
			n++
		}
	}
	return n
}

func percentChange(from, to float64) float64 {
	if from == 0 || math.IsNaN(from) {
		return 0
	}
	return 100 * (to - from) / from
}
