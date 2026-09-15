package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/credentials"
	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/notifications"
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

type pushTestCredentials struct{}

func (pushTestCredentials) Token(context.Context, string) (string, error) {
	return "worker-secret", nil
}
func (pushTestCredentials) Attributes(context.Context, string) (credentials.Attributes, error) {
	return credentials.Attributes{Kind: credentials.KindAPIKey, Provider: "worker"}, nil
}

func TestCloudflarePushEnrollmentRoutesStayBehindCoreAuth(t *testing.T) {
	var auth string
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/vapid-public-key":
			_ = json.NewEncoder(w).Encode(map[string]string{"publicKey": "BAbc"})
		case "/subscriptions", "/subscriptions/remove":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer worker.Close()

	h := newHarness(t)
	bus, err := events.New(h.store, h.store, events.Options{PollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	bus.Start(context.Background())
	t.Cleanup(bus.Stop)
	notes, err := notifications.New(context.Background(), h.store, h.store, bus, pushTestCredentials{}, nil, notifications.Options{
		PollInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	notes.Start(context.Background())
	t.Cleanup(notes.Stop)
	h.server.deps.Events = bus
	h.server.deps.Notifications = notes
	h.bootstrapAdmin()

	if rec := h.do(http.MethodPut, "/api/admin/notifications/channels/worker", map[string]any{
		"kind": "cloudflare", "enabled": true, "credentialId": "worker-token",
		"settings": map[string]string{"endpoint": worker.URL},
	}); rec.Code != http.StatusNoContent {
		t.Fatalf("put channel: %d %s", rec.Code, rec.Body)
	}

	key := h.do(http.MethodGet, "/api/admin/notifications/channels/worker/push-key", nil)
	if key.Code != http.StatusOK || key.Body.String() != `{"publicKey":"BAbc"}
` {
		t.Fatalf("push key: %d %q", key.Code, key.Body.String())
	}

	sub := map[string]any{
		"endpoint": "https://push.example.test/send?token=abc",
		"keys":     map[string]string{"p256dh": "AQ", "auth": "Ag"},
	}
	if rec := h.do(http.MethodPost, "/api/admin/notifications/channels/worker/push-subscriptions", sub); rec.Code != http.StatusNoContent {
		t.Fatalf("subscribe: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do(http.MethodPost, "/api/admin/notifications/channels/worker/push-subscriptions/remove", map[string]string{
		"endpoint": sub["endpoint"].(string),
	}); rec.Code != http.StatusNoContent {
		t.Fatalf("unsubscribe: %d %s", rec.Code, rec.Body)
	}
	if auth != "Bearer worker-secret" {
		t.Fatalf("worker authorization = %q", auth)
	}
}
