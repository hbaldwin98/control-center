package web

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hbaldwin98/control-center/host"
	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/pluginhost"
	"github.com/hbaldwin98/control-center/internal/core/policy"
)

func newPolicyHarness(t *testing.T) (*harness, *policy.Store) {
	t.Helper()
	h := newHarness(t)

	bus, err := events.New(h.store, h.store, events.Options{})
	if err != nil {
		t.Fatalf("events.New: %v", err)
	}
	p, err := policy.New(h.store, h.store, bus, nil)
	if err != nil {
		t.Fatalf("policy.New: %v", err)
	}
	h.server.deps.Events = bus
	h.server.deps.Policy = p
	return h, p
}

func TestPluginAdminRequiresAuth(t *testing.T) {
	h, _ := newPolicyHarness(t)
	if rec := h.do(http.MethodGet, "/api/admin/plugins", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
}

func TestPluginListEnableDisableAndBudget(t *testing.T) {
	h, p := newPolicyHarness(t)
	h.bootstrapAdmin()
	ctx := context.Background()

	if err := p.Register(ctx, "hello", false); err != nil {
		t.Fatal(err)
	}

	rec := h.do(http.MethodGet, "/api/admin/plugins", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	var snap struct {
		Data        []policy.State `json:"data"`
		AsOfEventID string         `json:"asOfEventId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.AsOfEventID == "" {
		t.Fatal("snapshot is missing asOfEventId")
	}
	if len(snap.Data) != 1 || snap.Data[0].PluginID != "hello" || snap.Data[0].Enabled {
		t.Fatalf("list = %+v", snap.Data)
	}

	if rec := h.do(http.MethodPut, "/api/admin/plugins/hello/budget",
		policy.Budget{Daily: 5_000_000, OnExceed: policy.ExceedReject}); rec.Code != http.StatusNoContent {
		t.Fatalf("budget: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do(http.MethodPost, "/api/admin/plugins/hello/enable", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("enable: %d %s", rec.Code, rec.Body)
	}

	rec = h.do(http.MethodGet, "/api/admin/plugins", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if !snap.Data[0].Enabled || snap.Data[0].Budget.Daily != 5_000_000 {
		t.Fatalf("after enable: %+v", snap.Data[0])
	}

	if rec := h.do(http.MethodPost, "/api/admin/plugins/hello/disable",
		map[string]string{"reason": "kill switch"}); rec.Code != http.StatusNoContent {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body)
	}

	st, err := p.State(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if st.Enabled || st.DisabledReason != "kill switch" {
		t.Fatalf("after disable: %+v", st)
	}
}

func TestAutomatedPluginCannotBeEnabledUncappedOverHTTP(t *testing.T) {
	h, p := newPolicyHarness(t)
	h.bootstrapAdmin()
	if err := p.Register(context.Background(), "hello", true); err != nil {
		t.Fatal(err)
	}

	rec := h.do(http.MethodPost, "/api/admin/plugins/hello/enable", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d %s, want 409", rec.Code, rec.Body)
	}
	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Error.Code != CodeConflict {
		t.Fatalf("code = %q", env.Error.Code)
	}
}

func TestPluginAdminMapsDomainErrors(t *testing.T) {
	h, p := newPolicyHarness(t)
	h.bootstrapAdmin()
	if err := p.Register(context.Background(), "hello", false); err != nil {
		t.Fatal(err)
	}

	if rec := h.do(http.MethodPost, "/api/admin/plugins/ghost/enable", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown: got %d, want 404", rec.Code)
	}
	if rec := h.do(http.MethodPut, "/api/admin/plugins/hello/budget",
		map[string]any{"hourly": -1, "daily": 0, "monthly": 0, "onExceed": "reject"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("negative: got %d %s, want 400", rec.Code, rec.Body)
	}
	if rec := h.do(http.MethodPut, "/api/admin/plugins/hello/budget",
		map[string]any{"hourly": 0, "daily": 0, "monthly": 0, "onExceed": "explode"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("onExceed: got %d %s, want 400", rec.Code, rec.Body)
	}
}

func TestBootstrapDoesNotListPolicyPlugins(t *testing.T) {
	h, p := newPolicyHarness(t)
	h.bootstrapAdmin()
	if err := p.Register(context.Background(), "hello", false); err != nil {
		t.Fatal(err)
	}

	rec := h.do(http.MethodGet, "/api/bootstrap", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap: %d %s", rec.Code, rec.Body)
	}
	var snap struct {
		Data shellBootstrap `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	// Shell reconciliation fails closed on a backend id with no frontend module.
	// Policy registration is not pluginhost registration.
	if len(snap.Data.Plugins) != 0 {
		t.Fatalf("bootstrap plugins = %+v, want empty until pluginhost", snap.Data.Plugins)
	}
}

func TestPluginListIncludesDeclaredEvents(t *testing.T) {
	h, _ := newPolicyHarness(t)
	h.bootstrapAdmin()

	reg, err := pluginhost.New(context.Background(), h.store, pluginhost.Options{
		DB: h.store, Policy: h.server.deps.Policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterAll(&modelPlugin{
		id: "fixture",
		events: []host.EventSpec{{
			Type: "synced", Purpose: "A collection finished.",
			Fields: []host.EventField{
				{Name: "day", Type: "string", Purpose: "Latest calendar day."},
				{Name: "body", Type: "string", Purpose: "One-line reading."},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	h.server.deps.PluginHost = reg

	rec := h.do(http.MethodGet, "/api/admin/plugins", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	var snap struct {
		Data []pluginView `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Data) != 1 || len(snap.Data[0].Events) != 1 {
		t.Fatalf("plugins = %+v", snap.Data)
	}
	ev := snap.Data[0].Events[0]
	if ev.Type != "fixture.synced" || ev.Match != "fixture.synced" || ev.Purpose == "" {
		t.Fatalf("event = %+v", ev)
	}
	if len(ev.Fields) != 2 || ev.Fields[0].Path != "event.payload.day" || ev.Fields[1].Name != "body" {
		t.Fatalf("fields = %+v", ev.Fields)
	}
}
