package tid

import (
	"testing"
)

func TestElectricAgreementsReturnsEveryElectricAgreement(t *testing.T) {
	body := []byte(`{"data":{"premiseList":[{"serviceAggrements":[{"saId":"electric-1","serviceType":"E"},{"saId":"gas","serviceType":"G"}]},{"serviceAggrements":{"saId":"electric-2","serviceType":"e"}}]}}`)
	agreements, err := electricAgreements(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(agreements) != 2 || stringValue(agreements[0], "saId") != "electric-1" || stringValue(agreements[1], "saId") != "electric-2" {
		t.Fatalf("unexpected agreements: %#v", agreements)
	}
}

func TestElectricAgreementsAcceptsCorrectlySpelledObject(t *testing.T) {
	body := []byte(`{"data":{"premiseList":{"serviceAgreements":{"saId":"electric-object","serviceType":"E","saRateSchedule":{"rateSchedule":"TEST"},"ServicePoints":{"meterId":"meter-test"}}}}}`)
	agreements, err := electricAgreements(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(agreements) != 1 || stringValue(agreements[0], "saId") != "electric-object" {
		t.Fatalf("unexpected agreements: %#v", agreements)
	}
}

func TestBillPeriodsReturnsEveryCompletePeriod(t *testing.T) {
	body := []byte(`{"data":{"billHistoryList":[{"usagePeriodStartDateTime":"2026-08-01","usagePeriodEndDateTime":"2026-08-31"},{"usagePeriodStartDateTime":"2026-07-01","usagePeriodEndDateTime":"2026-07-31"}]}}`)
	periods, err := billPeriods(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(periods) != 2 || periods[0].UsageStart != "2026-08-01" || periods[1].UsageStart != "2026-07-01" {
		t.Fatalf("unexpected periods: %#v", periods)
	}
}

func TestBillPeriodsRejectsMissingCompletePeriod(t *testing.T) {
	_, err := billPeriods([]byte(`{"data":{"billHistoryList":[{"usagePeriodStartDateTime":"start"}]}}`))
	if err == nil {
		t.Fatal("expected incomplete billing history to fail")
	}
}

func TestBillPeriodsDeduplicatesEquivalentDateRanges(t *testing.T) {
	body := []byte(`{"data":{"billHistoryList":[{"usagePeriodStartDateTime":"2026-07-01T00:00:00","usagePeriodEndDateTime":"2026-07-31T23:59:59"},{"usagePeriodStartDateTime":"2026-07-01","usagePeriodEndDateTime":"2026-07-31"}]}}`)
	periods, err := billPeriods(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(periods) != 1 {
		t.Fatalf("period count = %d, want 1: %#v", len(periods), periods)
	}
}

func TestParseUsageIncludesPeakBreakdownAndDemand(t *testing.T) {
	body := []byte(`{"data":{"usageList":[{"costDate":"2026-08-10","usage":"44.459167","dailyCost":4.91,"onPeakKwh":21.07856,"offPeakKwh":23.380607}],"demandInfo":{"peakDemandDate":"2026-08-24","peakDemandKw":7.92}}}`)
	readings, peakDate, peakKW, err := parseUsage(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(readings) != 1 || readings[0].OnPeakKWh == nil || *readings[0].OnPeakKWh != 21.07856 || readings[0].OffPeakKWh == nil || *readings[0].OffPeakKWh != 23.380607 {
		t.Fatalf("unexpected readings: %#v", readings)
	}
	if peakDate != "2026-08-24" || peakKW == nil || *peakKW != 7.92 {
		t.Fatalf("unexpected demand: %q %#v", peakDate, peakKW)
	}
}
