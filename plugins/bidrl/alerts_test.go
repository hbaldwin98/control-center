package bidrl_test

import (
	"context"
	"net/http"
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
