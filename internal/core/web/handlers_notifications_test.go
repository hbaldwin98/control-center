package web

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/hbaldwin98/control-center/host"
	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/notifications"
	"github.com/hbaldwin98/control-center/internal/core/pluginhost"
	"github.com/hbaldwin98/control-center/internal/core/policy"
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

func TestNotificationCatalogListsCoreAndPluginEvents(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin()

	pol, err := policy.New(h.store, h.store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := pluginhost.New(context.Background(), h.store, pluginhost.Options{
		DB: h.store, Policy: pol,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterAll(&modelPlugin{
		id: "fixture",
		events: []host.EventSpec{{
			Type: "synced", Purpose: "A collection finished.",
			Fields: []host.EventField{{Name: "body", Type: "string", Purpose: "One-line reading."}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	h.server.deps.PluginHost = reg

	rec := h.do(http.MethodGet, "/api/admin/notifications/catalog", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog: %d %s", rec.Code, rec.Body)
	}
	var snap struct {
		Data eventCatalog `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Data.Envelope) == 0 {
		t.Fatal("catalog is missing envelope paths")
	}
	var sawAlert, sawDead, sawPlugin bool
	for _, ev := range snap.Data.Events {
		switch ev.Match {
		case "*.alert":
			sawAlert = true
		case events.TypeJobDead:
			sawDead = true
		case "fixture.synced":
			sawPlugin = ev.Source == "fixture" && ev.Name == "Fixture"
			if len(ev.Fields) != 1 || ev.Fields[0].Path != "event.payload.body" {
				t.Fatalf("plugin fields = %+v", ev.Fields)
			}
		}
	}
	if !sawAlert || !sawDead || !sawPlugin {
		t.Fatalf("catalog events = %+v", snap.Data.Events)
	}
}
