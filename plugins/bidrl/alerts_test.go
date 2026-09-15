package bidrl_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hbaldwin98/control-center/host/hosttest"
	"github.com/hbaldwin98/control-center/plugins/bidrl"
)

func TestWarnAlertsOnceWhenAFavoriteIsEndingSoon(t *testing.T) {
	ctx := context.Background()
	h := hosttest.New(t, bidrl.New())
	h.Run(ctx)

	now := h.Clock.Now().UTC()
	insertLot(t, h, "fav-1", now.Add(2*time.Hour), now)
	insertLot(t, h, "other-1", now.Add(2*time.Hour), now)
	if rec := h.POST("/lots/fav-1/favorite", nil); rec.Code != http.StatusOK {
		t.Fatalf("favorite: %d %s", rec.Code, rec.Body.Bytes())
	}

	if err := h.RunJobNow(ctx, "warn", nil); err != nil {
		t.Fatalf("warn: %v", err)
	}
	if n := countEvents(h, "bidrl.alert"); n != 1 {
		t.Fatalf("ending-soon favorite alerts = %d, events %v", n, eventTypes(h))
	}

	if err := h.RunJobNow(ctx, "warn", nil); err != nil {
		t.Fatalf("second warn: %v", err)
	}
	if n := countEvents(h, "bidrl.alert"); n != 1 {
		t.Fatalf("warn re-alerted: %d alerts, events %v", n, eventTypes(h))
	}
}

func TestWarnSkipsLotsThatAreNotSaved(t *testing.T) {
	ctx := context.Background()
	h := hosttest.New(t, bidrl.New())
	h.Run(ctx)

	now := h.Clock.Now().UTC()
	insertLot(t, h, "plain-1", now.Add(2*time.Hour), now)

	if err := h.RunJobNow(ctx, "warn", nil); err != nil {
		t.Fatalf("warn: %v", err)
	}
	if n := countEvents(h, "bidrl.alert"); n != 0 {
		t.Fatalf("untracked lot alerted: events %v", eventTypes(h))
	}
}

// A lot first seen inside the narrowest configured window has missed the wider
// warnings. It should ring once at the nearest stage rather than firing every
// stage at once.
func TestWarnRingsOnlyLastCallWhenTheLotIsAlreadyClosing(t *testing.T) {
	ctx := context.Background()
	h := hosttest.New(t, bidrl.New())
	h.Run(ctx)

	now := h.Clock.Now().UTC()
	insertLot(t, h, "fav-1", now.Add(10*time.Minute), now)
	if rec := h.POST("/lots/fav-1/favorite", nil); rec.Code != http.StatusOK {
		t.Fatalf("favorite: %d %s", rec.Code, rec.Body.Bytes())
	}

	if err := h.RunJobNow(ctx, "warn", nil); err != nil {
		t.Fatalf("warn: %v", err)
	}
	bodies := alertBodies(h)
	if len(bodies) != 1 {
		t.Fatalf("alerts = %d, want 1: %v", len(bodies), bodies)
	}
	if !strings.Contains(bodies[0], "closes within 10 minutes") {
		t.Fatalf("not the 10-minute wording: %q", bodies[0])
	}

	// The day-ahead stage was overtaken, so a later tick must stay quiet.
	if err := h.RunJobNow(ctx, "warn", nil); err != nil {
		t.Fatalf("second warn: %v", err)
	}
	if n := len(alertBodies(h)); n != 1 {
		t.Fatalf("warn re-alerted: %d alerts", n)
	}
}

// A lot saved well ahead of time gets every configured warning exactly once as
// the clock crosses into each window.
func TestWarnUsesConfiguredLeadTimes(t *testing.T) {
	ctx := context.Background()
	h := hosttest.New(t, bidrl.New())
	h.Run(ctx)
	h.SetConfig(ctx, map[string]any{
		"savedAlertLeadTimes": []string{"2h", "10m"},
	})

	now := h.Clock.Now().UTC()
	insertLot(t, h, "fav-1", now.Add(90*time.Minute), now)
	if rec := h.POST("/lots/fav-1/favorite", nil); rec.Code != http.StatusOK {
		t.Fatalf("favorite: %d %s", rec.Code, rec.Body.Bytes())
	}
	if err := h.RunJobNow(ctx, "warn", nil); err != nil {
		t.Fatalf("configured warn: %v", err)
	}
	bodies := alertBodies(h)
	if len(bodies) != 1 || !strings.Contains(bodies[0], "closes within 2 hours") {
		t.Fatalf("configured first alert missing: %v", bodies)
	}

	h.Clock.Advance(80 * time.Minute) // ten minutes left
	if err := h.RunJobNow(ctx, "warn", nil); err != nil {
		t.Fatalf("configured narrow warn: %v", err)
	}
	bodies = alertBodies(h)
	if len(bodies) != 2 || !strings.Contains(bodies[1], "closes within 10 minutes") {
		t.Fatalf("configured second alert missing: %v", bodies)
	}
}

func TestWarnCanBeDisabledByAnEmptyLeadTimeList(t *testing.T) {
	ctx := context.Background()
	h := hosttest.New(t, bidrl.New())
	h.Run(ctx)
	h.SetConfig(ctx, map[string]any{"savedAlertLeadTimes": []string{}})

	now := h.Clock.Now().UTC()
	insertLot(t, h, "fav-1", now.Add(10*time.Minute), now)
	if rec := h.POST("/lots/fav-1/favorite", nil); rec.Code != http.StatusOK {
		t.Fatalf("favorite: %d %s", rec.Code, rec.Body.Bytes())
	}
	if err := h.RunJobNow(ctx, "warn", nil); err != nil {
		t.Fatalf("disabled warn: %v", err)
	}
	if n := len(alertBodies(h)); n != 0 {
		t.Fatalf("disabled alerts = %d", n)
	}
}

func TestWarnRingsTheDayAheadThenTheLastCall(t *testing.T) {
	ctx := context.Background()
	h := hosttest.New(t, bidrl.New())
	h.Run(ctx)

	now := h.Clock.Now().UTC()
	insertLot(t, h, "fav-1", now.Add(26*time.Hour), now)
	if rec := h.POST("/lots/fav-1/favorite", nil); rec.Code != http.StatusOK {
		t.Fatalf("favorite: %d %s", rec.Code, rec.Body.Bytes())
	}

	if err := h.RunJobNow(ctx, "warn", nil); err != nil {
		t.Fatalf("warn before first window: %v", err)
	}
	if n := len(alertBodies(h)); n != 0 {
		t.Fatalf("alerted before first window: %v", alertBodies(h))
	}

	h.Clock.Advance(3 * time.Hour) // 23 hours left
	if err := h.RunJobNow(ctx, "warn", nil); err != nil {
		t.Fatalf("24-hour warn: %v", err)
	}
	bodies := alertBodies(h)
	if len(bodies) != 1 || !strings.Contains(bodies[0], "closes within 24 hours") {
		t.Fatalf("24-hour alert missing: %v", bodies)
	}

	h.Clock.Advance(19 * time.Hour) // four hours left
	if err := h.RunJobNow(ctx, "warn", nil); err != nil {
		t.Fatalf("4-hour warn: %v", err)
	}
	bodies = alertBodies(h)
	if len(bodies) != 2 || !strings.Contains(bodies[1], "closes within 4 hours") {
		t.Fatalf("4-hour alert missing: %v", bodies)
	}

	h.Clock.Advance(3 * time.Hour) // one hour left
	if err := h.RunJobNow(ctx, "warn", nil); err != nil {
		t.Fatalf("1-hour warn: %v", err)
	}
	bodies = alertBodies(h)
	if len(bodies) != 3 || !strings.Contains(bodies[2], "closes within 1 hour") {
		t.Fatalf("1-hour alert missing: %v", bodies)
	}

	h.Clock.Advance(50 * time.Minute) // ten minutes left
	if err := h.RunJobNow(ctx, "warn", nil); err != nil {
		t.Fatalf("10-minute warn: %v", err)
	}
	bodies = alertBodies(h)
	if len(bodies) != 4 || !strings.Contains(bodies[3], "closes within 10 minutes") {
		t.Fatalf("10-minute alert missing: %v", bodies)
	}

	// Every configured stage has now fired; nothing further should ring.
	h.Clock.Advance(5 * time.Minute)
	if err := h.RunJobNow(ctx, "warn", nil); err != nil {
		t.Fatalf("final warn: %v", err)
	}
	if n := len(alertBodies(h)); n != 4 {
		t.Fatalf("warn re-alerted: %d alerts", n)
	}
}

// alertBodies returns the body of every bidrl.alert, in the order published.
func alertBodies(h *hosttest.Harness) []string {
	var out []string
	for _, e := range h.Events() {
		if e.Type != "bidrl.alert" {
			continue
		}
		var payload struct {
			Body string `json:"body"`
		}
		_ = json.Unmarshal(e.Payload, &payload)
		out = append(out, payload.Body)
	}
	return out
}

func insertLot(t *testing.T, h *hosttest.Harness, id string, ends, created time.Time) {
	t.Helper()
	if _, err := h.DB().Exec(`INSERT INTO bidrl_lots(id, auction_id, url, title, bucket, created_at, ends_at)
		VALUES (?, '42', ?, ?, 'pending', ?, ?)`,
		id, "https://www.bidrl.com/auction/42/item/"+id, "Lot "+id,
		created.Format(time.RFC3339Nano), ends.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
}

func countEvents(h *hosttest.Harness, typ string) int {
	n := 0
	for _, e := range h.Events() {
		if e.Type == typ {
			n++
		}
	}
	return n
}
