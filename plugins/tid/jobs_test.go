package tid

import (
	"testing"
	"time"
)

func TestSyncedPayloadSkipsTheUnpopulatedCurrentDay(t *testing.T) {
	now := time.Date(2026, 9, 2, 6, 0, 0, 0, localZone())
	readings := []Reading{
		{Day: "2026-08-31", KWh: 11},
		{Day: "2026-09-01", KWh: 9.5},
		{Day: "2026-09-02", KWh: 0},
	}
	payload := syncedPayload(readings, now, "at", 3, "portal")

	if payload.Latest == nil || payload.Latest.Day != "2026-09-02" {
		t.Fatalf("latest = %#v, want the newest day", payload.Latest)
	}
	if payload.Today == nil || payload.Today.KWh != 0 {
		t.Fatalf("today = %#v, want the empty current day", payload.Today)
	}
	if payload.Yesterday == nil || payload.Yesterday.Day != "2026-09-01" || payload.Yesterday.KWh != 9.5 {
		t.Fatalf("yesterday = %#v, want 2026-09-01 at 9.5 kWh", payload.Yesterday)
	}
	if payload.Settled == nil || payload.Settled.Day != "2026-09-01" {
		t.Fatalf("settled = %#v, want the newest day with usage", payload.Settled)
	}
	if payload.Body != payload.Settled.Body {
		t.Fatalf("body = %q, want the settled day %q", payload.Body, payload.Settled.Body)
	}
	if len(payload.Recent) != 3 || payload.Recent[0].Day != "2026-09-02" || payload.Recent[2].Day != "2026-08-31" {
		t.Fatalf("recent = %#v, want all three days newest first", payload.Recent)
	}
}

func TestSyncedPayloadWithoutReadings(t *testing.T) {
	payload := syncedPayload(nil, time.Now(), "at", 0, "portal")
	if payload.Latest != nil || payload.Yesterday != nil || payload.Settled != nil {
		t.Fatalf("payload = %#v, want no days", payload)
	}
	if payload.Body != "TID synced, no daily reading yet" || len(payload.Recent) != 0 {
		t.Fatalf("payload = %#v", payload)
	}
}
