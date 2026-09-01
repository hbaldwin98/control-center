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

func TestCurrentBillPeriodUsesOnlyFirstCompletePeriod(t *testing.T) {
	body := []byte(`{"data":{"billHistoryList":[{"usagePeriodStartDateTime":"current-start","usagePeriodEndDateTime":"current-end"},{"usagePeriodStartDateTime":"prior-start","usagePeriodEndDateTime":"prior-end"}]}}`)
	period, err := currentBillPeriod(body)
	if err != nil {
		t.Fatal(err)
	}
	if period.UsageStart != "current-start" || period.UsageEnd != "current-end" {
		t.Fatalf("unexpected period: %#v", period)
	}
}

func TestCurrentBillPeriodRejectsMissingCompletePeriod(t *testing.T) {
	_, err := currentBillPeriod([]byte(`{"data":{"billHistoryList":[{"usagePeriodStartDateTime":"start"}]}}`))
	if err == nil {
		t.Fatal("expected incomplete billing history to fail")
	}
}
