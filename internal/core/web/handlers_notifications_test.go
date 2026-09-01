package web

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/notifications"
)

func TestInboxListsDefaultRuleMatch(t *testing.T) {
	h := newHarness(t)
	bus, err := events.New(h.store, h.store, events.Options{PollInterval: 15 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	bus.Start(context.Background())
	t.Cleanup(bus.Stop)
	notes, err := notifications.New(context.Background(), h.store, h.store, bus, nil, nil, notifications.Options{
		PollInterval: 15 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	notes.Start(context.Background())
	t.Cleanup(notes.Stop)
	h.server.deps.Events = bus
	h.server.deps.Notifications = notes
	h.bootstrapAdmin()

	if _, err := bus.Publish(context.Background(), events.Input{
		Type: events.TypeJobDead, Source: events.SourceJobs, Subject: "hello.tick",
	}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var rec struct {
		Data struct {
			Notifications []struct {
				Title string `json:"title"`
			} `json:"notifications"`
		} `json:"data"`
	}
	for time.Now().Before(deadline) {
		resp := h.do("GET", "/api/notifications", nil)
		if resp.Code != 200 {
			t.Fatalf("inbox %d: %s", resp.Code, resp.Body.Bytes())
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &rec); err != nil {
			t.Fatal(err)
		}
		if len(rec.Data.Notifications) == 1 {
			if rec.Data.Notifications[0].Title != "Job exhausted retries" {
				t.Fatalf("%+v", rec)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("inbox never populated")
}
