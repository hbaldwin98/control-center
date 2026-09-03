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
		"100-104F",
		"MONTH TO DATE",
		"kWh/day",
		"versus what those temperatures predict",
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

// The complaint this guards against: an insight that waves off the current
// month as unfinished instead of comparing it. Every month-to-date row covers
// the same span of day numbers, so the comparison is always available.
func TestMonthToDateComparesEqualDayRanges(t *testing.T) {
	now := time.Date(2026, 9, 11, 7, 0, 0, 0, localZone())
	history := buildDays(now, 500)
	// Push this month's first ten days 50% above the rest.
	for i := range history {
		if strings.HasPrefix(history[i].Day, "2026-09") {
			history[i].KWh = 60
		}
	}

	got := insightPrompt(now, history[:30], history, nil, settings{})

	if !strings.Contains(got, "MONTH TO DATE (days 1-10 of each month") {
		t.Fatalf("month-to-date should be clipped to the days this month actually has:\n%s", got)
	}
	for _, want := range []string{
		"this month:",
		"previous month:",
		"same month last year:",
		"60.0 kWh/day over 10 days",
		"(this month is +50% per day)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("month-to-date is missing %q\n%s", want, got)
		}
	}
}

func TestMonthToDateSkippedWithoutCurrentMonthData(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 30, 0, 0, localZone())
	days := []dayView{{Day: "2026-08-30", KWh: 10}, {Day: "2026-08-31", KWh: 11}}
	if got := insightPrompt(now, days, days, nil, settings{}); strings.Contains(got, "MONTH TO DATE") {
		t.Fatalf("no current-month readings, so there is nothing to date:\n%s", got)
	}
}
