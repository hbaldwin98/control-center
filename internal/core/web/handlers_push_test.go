package web

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/policy"
	"github.com/hbaldwin98/control-center/internal/core/push"
)

func newPushHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	ctx := context.Background()
	bus, err := events.New(h.store, h.store, events.Options{PollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	bus.Start(ctx)
	t.Cleanup(bus.Stop)
	pol, err := policy.New(h.store, h.store, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := pol.Register(ctx, "hello", false); err != nil {
		t.Fatal(err)
	}
	if err := pol.Enable(ctx, "hello", "test", "go"); err != nil {
		t.Fatal(err)
	}
	svc, err := push.New(pol, push.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	h.server.deps.Push = svc
	h.server.deps.Plugins = func(context.Context) []PluginDescriptor {
		return []PluginDescriptor{{ID: "hello", Name: "Hello", Enabled: true}}
	}
	h.bootstrapAdmin()
	return h
}

func TestPushRequiresAuthentication(t *testing.T) {
	h := newHarness(t)
	rec := h.do(http.MethodGet, "/api/push/hello?topics=lot:1", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// An id that names no registered plugin must not be able to hold a connection, or an
// unknown name would be a free way to occupy one.
func TestPushRejectsAnUnknownPlugin(t *testing.T) {
	h := newPushHarness(t)
	rec := h.do(http.MethodGet, "/api/push/nosuch?topics=lot:1", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body)
	}
}

func TestPushRejectsAnEmptyWatchSet(t *testing.T) {
	h := newPushHarness(t)
	rec := h.do(http.MethodGet, "/api/push/hello", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestPushIsUnavailableWithoutTheService(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin()
	rec := h.do(http.MethodGet, "/api/push/hello?topics=lot:1", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body)
	}
}
