package tid

import (
	"strings"
	"testing"
	"time"
)

func f(v float64) *float64 { return &v }

func cents(v int64) *int64 { return &v }

// buildDays makes n days ending on the day before `now`, warm in summer and
// mild in winter, so the prompt has both a seasonal history and a recent window.
func buildDays(now time.Time, n int) []dayView {
	out := make([]dayView, 0, n)
	for i := 1; i <= n; i++ {
		day := now.AddDate(0, 0, -i)
		high := 60.0
		kwh := 12.0
		if m := day.Month(); m >= time.June && m <= time.September {
			high = 100
			kwh = 40
		}
		out = append(out, dayView{
			Day: day.Format("2006-01-02"), KWh: kwh, CostCents: cents(int64(kwh * 12)),
			OnPeakKWh: f(kwh / 4), OffPeakKWh: f(kwh * 3 / 4),
			HighTempF: f(high), LowTempF: f(high - 30), AvgTempF: f(high - 15),
		})
	}
	return out
}

func TestInsightPromptCarriesHistoryAndTemperature(t *testing.T) {
	now := time.Date(2026, 9, 2, 7, 0, 0, 0, localZone())
	history := buildDays(now, 500)
	recent := history[:30]
	months := []monthStat{
		{Month: "2025-09", KWh: 1200, CostCents: cents(14400), Days: 30, AvgHighF: f(99)},
		{Month: "2026-08", KWh: 1240, CostCents: cents(14880), Days: 31, AvgHighF: f(100)},
		{Month: "2026-09", KWh: 40, Days: 1, AvgHighF: f(100)},
	}

	got := insightPrompt(now, recent, history, months, settings{CentsPerKWh: 12})

	for _, want := range []string{
		"MONTHLY HISTORY",
		"2025-09",
		"month in progress",
		"Same month last year",
		"USAGE BY OUTDOOR HIGH TEMPERATURE",
		"100F and up",
		"WINDOW COMPARISON",
		"the same 30 days last year",
		"LAST 30 DAYS",
		"expected",
		"on-peak",
		"12.0 cents/kWh",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt is missing %q\n%s", want, got)
		}
	}
	if lines := strings.Count(got, "\n"); lines < 50 {
		t.Fatalf("prompt looks too thin (%d lines):\n%s", lines, got)
	}
}

func TestInsightPromptSurvivesMissingTemperatures(t *testing.T) {
	now := time.Date(2026, 9, 2, 7, 0, 0, 0, localZone())
	days := []dayView{
		{Day: "2026-08-30", KWh: 10},
		{Day: "2026-08-31", KWh: 11},
		{Day: "2026-09-01", KWh: 12},
	}
	got := insightPrompt(now, days, days, nil, settings{})
	if !strings.Contains(got, "cannot be temperature-adjusted") {
		t.Fatalf("prompt should say temperatures are missing:\n%s", got)
	}
	if !strings.Contains(got, "no temp") {
		t.Fatalf("days without weather should be marked:\n%s", got)
	}
	if strings.Contains(got, "expected") {
		t.Fatalf("no baseline exists, so no day should carry an expectation:\n%s", got)
	}
}

func TestExpectedKWhIgnoresThinBands(t *testing.T) {
	bands := []tempBand{{low: 90, high: 100, days: 2, kwh: 60}, {low: 100, high: 1000, days: 10, kwh: 400}}
	if _, ok := expectedKWh(bands, f(95)); ok {
		t.Fatal("a two-day band is not a baseline")
	}
	got, ok := expectedKWh(bands, f(104))
	if !ok || got != 40 {
		t.Fatalf("expected = %v (%v), want 40", got, ok)
	}
	if _, ok := expectedKWh(bands, nil); ok {
		t.Fatal("a day without a temperature has no expectation")
	}
}
