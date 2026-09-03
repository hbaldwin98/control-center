package tid

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/hbaldwin98/control-center/host/hosttest"
	"time"
)

// samplePayloads is one fully populated value per declared event, so every path
// the catalog offers has something to resolve against.
func samplePayloads(t *testing.T) map[string]any {
	t.Helper()
	now := time.Date(2026, 9, 2, 6, 0, 0, 0, localZone())
	high, low := 101.0, 68.0
	cost := int64(491)
	onPeak, offPeak := 21.0, 23.4
	// Every day carries every optional field: the catalog promises these paths,
	// so the sample has to offer all of them.
	readings := make([]Reading, 0, 3)
	for i := range 3 {
		readings = append(readings, Reading{
			Day: now.AddDate(0, 0, -i).Format("2006-01-02"), KWh: 44.4,
			CostCents: &cost, OnPeakKWh: &onPeak, OffPeakKWh: &offPeak,
			HighTempF: &high, LowTempF: &low, AvgTempF: &high,
		})
	}
	sync := syncedPayload(readings, now, now.Format(time.RFC3339Nano), 3, "portal")
	return map[string]any{
		"synced": sync,
		"alert":  alertPayload(sync, "3 new daily readings", 3),
		"insight": insightPayload{
			At: now.Format(time.RFC3339Nano), Summary: "s", Recommendation: "r",
			Anomalies: []string{"a"}, Body: "b",
		},
	}
}

// TestCatalogMatchesThePayloads is the guard against drift. The assertions live
// in hosttest so every plugin makes the same promise in one line.
func TestCatalogMatchesThePayloads(t *testing.T) {
	hosttest.CheckEventCatalog(t, New().Manifest(), samplePayloads(t))
}

// TestSyncedAndAlertShareTheirDays keeps the two events interchangeable for a
// rule template, which is the reason they embed the same struct.
func TestSyncedAndAlertShareTheirDays(t *testing.T) {
	samples := samplePayloads(t)
	sync := samples["synced"].(synced)
	alert := samples["alert"].(alerted)
	if !reflect.DeepEqual(alert.days, sync.days) {
		t.Fatalf("alert days = %+v, want the synced days %+v", alert.days, sync.days)
	}
	for key := range dayPurposes("rows") {
		if key == "at" {
			continue // the sync timestamp is a synced-only field.
		}
		if resolve(marshalMap(t, alert), key) == nil {
			t.Errorf("alert is missing the shared path %q", key)
		}
	}
}

func marshalMap(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// resolve walks a dotted payload path the way a notification template does.
func resolve(payload map[string]any, path string) any {
	var cur any = payload
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[part]
	}
	return cur
}
