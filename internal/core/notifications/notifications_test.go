package notifications

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

type nfix struct {
	t     *testing.T
	ctx   context.Context
	store *storage.Store
	bus   *events.Log
	svc   *Service
}

func newNfix(t *testing.T, httpClient *http.Client) *nfix {
	t.Helper()
	ctx := context.Background()
	st, err := storage.Open(ctx, storage.Options{Path: filepath.Join(t.TempDir(), "n.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bus, err := events.New(st, st, events.Options{PollInterval: 15 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	bus.Start(ctx)
	t.Cleanup(bus.Stop)
	svc, err := New(ctx, st, st, bus, nil, nil, Options{
		PollInterval: 15 * time.Millisecond,
		HTTPClient:   httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.Start(ctx)
	t.Cleanup(svc.Stop)
	return &nfix{t: t, ctx: ctx, store: st, bus: bus, svc: svc}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestDefaultRulesDeliverJobDead(t *testing.T) {
	f := newNfix(t, nil)
	if _, err := f.bus.Publish(f.ctx, events.Input{
		Type: events.TypeJobDead, Source: events.SourceJobs, Subject: "hello.tick",
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "inbox row", func() bool {
		page, err := f.svc.List(f.ctx, InboxQuery{Limit: 20})
		return err == nil && len(page.Notifications) == 1
	})
	page, _ := f.svc.List(f.ctx, InboxQuery{Limit: 20})
	n := page.Notifications[0]
	if n.Title != "Job exhausted retries" || n.Subject != "hello.tick" || n.Read {
		t.Fatalf("notification = %+v", n)
	}
	if err := f.svc.MarkRead(f.ctx, n.ID, true); err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.Get(f.ctx, n.ID)
	if err != nil || !got.Read {
		t.Fatalf("read = %+v err=%v", got, err)
	}
}

func TestPluginAlertExcludesCore(t *testing.T) {
	f := newNfix(t, nil)
	if _, err := f.bus.Publish(f.ctx, events.Input{
		Type: "hello.alert", Source: "hello", Subject: "boom",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.bus.Publish(f.ctx, events.Input{
		Type: "core.jobs.alert", Source: events.SourceJobs, Subject: "nope",
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "plugin alert only", func() bool {
		page, err := f.svc.List(f.ctx, InboxQuery{Limit: 20})
		return err == nil && len(page.Notifications) == 1 && page.Notifications[0].Subject == "boom"
	})
}

func TestNotificationEventsDoNotRecurse(t *testing.T) {
	f := newNfix(t, nil)
	ctx := WithActor(f.ctx, "admin")
	if err := f.svc.PutRule(ctx, Rule{
		ID: "all-events", Enabled: true, Match: "**", Channels: []string{"inbox"},
		Title: "saw {event.type}", Body: "",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.bus.Publish(f.ctx, events.Input{
		Type: events.TypeJobDead, Source: events.SourceJobs, Subject: "x",
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "ready event handled", func() bool {
		page, _ := f.svc.List(f.ctx, InboxQuery{Limit: 50})
		return len(page.Notifications) >= 2
	})
	time.Sleep(80 * time.Millisecond)
	page, _ := f.svc.List(f.ctx, InboxQuery{Limit: 50})
	for _, n := range page.Notifications {
		if strings.Contains(n.Title, "core.notification.") {
			t.Fatalf("recursed into %q", n.Title)
		}
	}
}

func TestThrottleCollapsesSameSubject(t *testing.T) {
	f := newNfix(t, nil)
	ctx := WithActor(f.ctx, "admin")
	if err := f.svc.PutRule(ctx, Rule{
		ID: "throttled", Enabled: true, Match: "hello.tick", Channels: []string{"inbox"},
		Title: "ticks {collapsed}", Body: "{event.subject}", Throttle: 2 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := f.bus.Publish(f.ctx, events.Input{
			Type: "hello.tick", Source: "hello", Subject: "same",
		}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "window closed", func() bool {
		page, _ := f.svc.List(f.ctx, InboxQuery{Limit: 20})
		return len(page.Notifications) == 1 && page.Notifications[0].Collapsed == 2
	})
}

func TestRetryIsIdempotent(t *testing.T) {
	f := newNfix(t, nil)
	id, err := f.bus.Publish(f.ctx, events.Input{
		Type: events.TypeJobDead, Source: events.SourceJobs, Subject: "once",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first delivery", func() bool {
		page, _ := f.svc.List(f.ctx, InboxQuery{Limit: 20})
		return len(page.Notifications) == 1
	})
	if err := f.store.Tx(f.ctx, func(tx storage.Tx) error {
		return f.svc.handleEvent(f.ctx, tx, events.Event{
			ID: id, Type: events.TypeJobDead, Source: events.SourceJobs, Subject: "once",
		})
	}); err != nil {
		t.Fatal(err)
	}
	page, _ := f.svc.List(f.ctx, InboxQuery{Limit: 20})
	if len(page.Notifications) != 1 {
		t.Fatalf("got %d notifications after retry", len(page.Notifications))
	}
}

func TestNtfySendUsesIdempotencyKey(t *testing.T) {
	var hits atomic.Int32
	var gotKey, gotTitle string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		gotKey = r.Header.Get("Idempotency-Key")
		gotTitle = r.Header.Get("Title")
		body, _ := io.ReadAll(r.Body)
		if string(body) != "hello.tick" {
			t.Errorf("body = %q", body)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	f := newNfix(t, srv.Client())
	ctx := WithActor(f.ctx, "admin")
	if err := f.svc.PutChannel(ctx, ChannelConfig{
		ID: "ops-ntfy", Kind: "ntfy", Enabled: true,
		Settings: map[string]string{"server": srv.URL, "topic": "ops"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.PutRule(ctx, Rule{
		ID: "to-ntfy", Enabled: true, Match: "hello.tick",
		Channels: []string{"inbox", "ops-ntfy"}, Title: "Tick", Body: "{event.subject}",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.bus.Publish(f.ctx, events.Input{
		Type: "hello.tick", Source: "hello", Subject: "hello.tick",
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "ntfy POST", func() bool { return hits.Load() >= 1 })
	if gotKey == "" || gotTitle != "Tick" {
		t.Fatalf("key=%q title=%q", gotKey, gotTitle)
	}
}

func TestRejectsSecretChannelSettingsAndBadURL(t *testing.T) {
	f := newNfix(t, nil)
	ctx := WithActor(f.ctx, "admin")
	if err := f.svc.PutChannel(ctx, ChannelConfig{
		ID: "bad", Kind: "ntfy", Enabled: true,
		Settings: map[string]string{"topic": "x", "token": "abc"},
	}); err == nil {
		t.Fatal("expected secret setting rejection")
	}
	if err := f.svc.PutRule(ctx, Rule{
		ID: "bad-url", Enabled: true, Match: "hello.tick", Channels: []string{"inbox"},
		Title: "x", URL: "https://evil.example/phish",
	}); err == nil {
		t.Fatal("expected url rejection")
	}
	if err := f.svc.PutRule(ctx, Rule{
		ID: "ok-url", Enabled: true, Match: "hello.tick", Channels: []string{"inbox"},
		Title: "x", URL: "/jobs?id=1",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPutRuleRequiresActor(t *testing.T) {
	f := newNfix(t, nil)
	err := f.svc.PutRule(f.ctx, Rule{ID: "x", Enabled: true, Match: "hello.tick", Channels: []string{"inbox"}, Title: "t"})
	if err != ErrNoActor {
		t.Fatalf("err = %v", err)
	}
}

func TestWhereCompileRejectedAtSave(t *testing.T) {
	f := newNfix(t, nil)
	err := f.svc.PutRule(WithActor(f.ctx, "admin"), Rule{
		ID: "bad-where", Enabled: true, Match: "hello.tick", Channels: []string{"inbox"},
		Title: "t", Where: "event.type + 1",
	})
	if err == nil {
		t.Fatal("expected where rejection")
	}
}
