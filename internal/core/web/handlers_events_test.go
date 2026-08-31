package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/events"
)

func newEventsHarness(t *testing.T) (*harness, *events.Log) {
	t.Helper()
	h := newHarness(t)
	bus, err := events.New(h.store, h.store, events.Options{PollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	bus.Start(ctx)
	t.Cleanup(func() { cancel(); bus.Stop() })
	h.server.deps.Events = bus
	return h, bus
}

func waitPaused(t *testing.T, bus *events.Log, name string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		subs, err := bus.Subscribers(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range subs {
			if s.Name == name && s.State == events.CursorPaused {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("subscriber %q did not pause", name)
}

func TestEventsQueryRejectsBadAfterAndLimit(t *testing.T) {
	h, _ := newEventsHarness(t)
	h.bootstrapAdmin()

	if rec := h.do(http.MethodGet, "/api/events?after=nope", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad after: got %d, want 400", rec.Code)
	}
	if rec := h.do(http.MethodGet, "/api/events?limit=0", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("limit 0: got %d, want 400", rec.Code)
	}
	if rec := h.do(http.MethodGet, "/api/events?limit=1001", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("limit 1001: got %d, want 400", rec.Code)
	}
}

func TestSubscriberActionsRetrySkipAndReset(t *testing.T) {
	h, bus := newEventsHarness(t)
	h.bootstrapAdmin()
	ctx := context.Background()

	fail := errors.New("poison")
	if _, err := bus.SubscribeDurable(events.DurableConfig{
		Name:    "poison.consumer",
		Pattern: "core.job.*",
		Start:   events.CursorStart{Mode: events.FromNow},
		Retry:   events.RetryPolicy{MaxAttempts: 1, Initial: time.Millisecond, Maximum: time.Millisecond},
	}, func(context.Context, events.Event) error { return fail }); err != nil {
		t.Fatal(err)
	}
	if _, err := bus.Publish(ctx, events.Input{Type: "core.job.failed", Source: events.SourceJobs}); err != nil {
		t.Fatal(err)
	}
	waitPaused(t, bus, "poison.consumer")

	rec := h.do(http.MethodGet, "/api/events/subscribers", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	var listed struct {
		Data []events.SubscriberStatus `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Data) != 1 || listed.Data[0].State != events.CursorPaused {
		t.Fatalf("subscribers = %+v", listed.Data)
	}

	if rec := h.do(http.MethodPost, "/api/events/subscribers/poison.consumer/retry", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body)
	}
	waitPaused(t, bus, "poison.consumer")

	if rec := h.do(http.MethodPost, "/api/events/subscribers/poison.consumer/skip", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("skip: %d %s", rec.Code, rec.Body)
	}

	subs, err := bus.Subscribers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].State != events.CursorActive {
		t.Fatalf("after skip: %+v", subs)
	}

	if rec := h.do(http.MethodPost, "/api/events/subscribers/poison.consumer/reset",
		map[string]string{"mode": "beginning"}); rec.Code != http.StatusNoContent {
		t.Fatalf("reset: %d %s", rec.Code, rec.Body)
	}
}

func TestSubscriberActionErrors(t *testing.T) {
	h, bus := newEventsHarness(t)
	h.bootstrapAdmin()

	if _, err := bus.SubscribeDurable(events.DurableConfig{
		Name:    "healthy",
		Pattern: "core.job.*",
		Start:   events.CursorStart{Mode: events.FromNow},
		Retry:   events.RetryPolicy{MaxAttempts: 1, Initial: time.Millisecond, Maximum: time.Millisecond},
	}, func(context.Context, events.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}

	if rec := h.do(http.MethodPost, "/api/events/subscribers/healthy/skip", nil); rec.Code != http.StatusConflict {
		t.Fatalf("skip active: got %d, want 409", rec.Code)
	}
	if rec := h.do(http.MethodPost, "/api/events/subscribers/missing/retry", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown name: got %d, want 404", rec.Code)
	}
	if rec := h.do(http.MethodPost, "/api/events/subscribers/healthy/explode", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown action: got %d, want 404", rec.Code)
	}
	if rec := h.do(http.MethodPost, "/api/events/subscribers/healthy/reset",
		map[string]string{"mode": "after_event", "eventId": "-1"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad eventId: got %d, want 400", rec.Code)
	}
}
