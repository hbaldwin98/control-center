package web

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hbaldwin98/control-center/host"
	"github.com/hbaldwin98/control-center/internal/core/ai"
	"github.com/hbaldwin98/control-center/internal/core/credentials"
	"github.com/hbaldwin98/control-center/internal/core/pluginhost"
	"github.com/hbaldwin98/control-center/internal/core/policy"
)

// newAIAdminHarness gives the server a live ai service with no providers and no routes,
// which is what a fresh installation looks like before anything is configured.
func newAIAdminHarness(t *testing.T) (*harness, *credentials.Store) {
	t.Helper()
	h, creds := newCredsHarness(t, nil)
	h.bootstrapAdmin()
	h.reauth()

	pol, err := policy.New(h.store, h.store, h.server.deps.Events, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := ai.New(h.store, h.store, h.server.deps.Events, pol, creds, ai.Options{Refs: creds})
	if err != nil {
		t.Fatal(err)
	}
	h.server.deps.Policy = pol
	h.server.deps.AI = svc
	return h, creds
}

func (h *harness) createKey(t *testing.T, id string) {
	t.Helper()
	if rec := h.do(http.MethodPost, "/api/admin/credentials",
		map[string]string{"id": id, "provider": "fake", "secret": "test-token"}); rec.Code != http.StatusCreated {
		t.Fatalf("create credential: %d %s", rec.Code, rec.Body)
	}
}

func TestAIAdminProviderAndRouteLifecycle(t *testing.T) {
	h, _ := newAIAdminHarness(t)
	h.createKey(t, "fake-key")

	rec := h.do(http.MethodPut, "/api/admin/ai/providers/local", map[string]any{
		"kind": "fake", "credentialId": "fake-key", "billing": "metered",
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("put provider: %d %s", rec.Code, rec.Body)
	}

	rec = h.do(http.MethodGet, "/api/admin/ai/providers", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list providers: %d %s", rec.Code, rec.Body)
	}
	var snap struct {
		Data []ai.ProviderConfig `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Data) != 1 || snap.Data[0].ID != "local" {
		t.Fatalf("providers = %+v", snap.Data)
	}

	// Discovery goes to the adapter, which for the fake is in process.
	rec = h.do(http.MethodGet, "/api/admin/ai/providers/local/models?refresh=1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("models: %d %s", rec.Code, rec.Body)
	}
	var models []ai.Model
	if err := json.Unmarshal(rec.Body.Bytes(), &models); err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "echo" {
		t.Fatalf("models = %+v", models)
	}

	rec = h.do(http.MethodPut, "/api/admin/ai/routes/cheap-chat", map[string]any{
		"capabilities": []string{"chat"}, "maxInputTokens": 128, "maxOutputTokens": 64,
		"attempts": []map[string]any{{
			"provider": "local", "model": "echo",
			"inputMicroUsdPerMillion": 1_000_000, "outputMicroUsdPerMillion": 2_000_000,
		}},
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("put route: %d %s", rec.Code, rec.Body)
	}

	// The provider is now load-bearing. Deleting it would turn a working route into a
	// broken one behind the administrator's back.
	if rec := h.do(http.MethodDelete, "/api/admin/ai/providers/local", nil); rec.Code != http.StatusConflict {
		t.Fatalf("delete provider in use: %d %s", rec.Code, rec.Body)
	}

	rec = h.do(http.MethodGet, "/api/admin/ai/routes", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("routes: %d %s", rec.Code, rec.Body)
	}
	var routes struct {
		Data []ai.RouteDescriptor `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &routes); err != nil {
		t.Fatal(err)
	}
	if len(routes.Data) != 1 || !routes.Data[0].Healthy {
		t.Fatalf("routes = %+v", routes.Data)
	}
	r := routes.Data[0]
	if r.MaxInputTokens != 128 || len(r.AttemptPlan) != 1 || r.AttemptPlan[0].InputMicroUSDPerMillion != 1_000_000 {
		t.Fatalf("route did not round-trip: %+v", r)
	}

	if rec := h.do(http.MethodDelete, "/api/admin/ai/routes/cheap-chat", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete route: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do(http.MethodDelete, "/api/admin/ai/providers/local", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete provider: %d %s", rec.Code, rec.Body)
	}
}

func TestAIAdminRejectsUnusableConfiguration(t *testing.T) {
	h, _ := newAIAdminHarness(t)
	h.createKey(t, "fake-key")

	cases := []struct {
		name string
		path string
		body map[string]any
		want int
	}{
		{
			name: "unknown credential",
			path: "/api/admin/ai/providers/local",
			body: map[string]any{"kind": "fake", "credentialId": "nope", "billing": "metered"},
			want: http.StatusBadRequest,
		},
		{
			name: "codex demands an oauth credential, not an api key",
			path: "/api/admin/ai/providers/chatgpt",
			body: map[string]any{"kind": "codex", "credentialId": "fake-key"},
			want: http.StatusBadRequest,
		},
		{
			name: "plain http off loopback",
			path: "/api/admin/ai/providers/remote",
			body: map[string]any{"kind": "openai_compatible", "baseUrl": "http://models.example/v1", "credentialId": "fake-key"},
			want: http.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if rec := h.do(http.MethodPut, tc.path, tc.body); rec.Code != tc.want {
				t.Fatalf("got %d %s, want %d", rec.Code, rec.Body, tc.want)
			}
		})
	}

	if rec := h.do(http.MethodGet, "/api/admin/ai/providers/missing/models", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("models for unknown provider: %d %s", rec.Code, rec.Body)
	}

	// A metered attempt with no price is exactly the request that could dispatch
	// unbounded, so it is refused at the edit rather than at the call.
	h.do(http.MethodPut, "/api/admin/ai/providers/local", map[string]any{
		"kind": "fake", "credentialId": "fake-key", "billing": "metered",
	})
	rec := h.do(http.MethodPut, "/api/admin/ai/routes/unpriced", map[string]any{
		"capabilities": []string{"chat"}, "maxInputTokens": 128, "maxOutputTokens": 64,
		"attempts": []map[string]any{{"provider": "local", "model": "echo"}},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unpriced route: %d %s", rec.Code, rec.Body)
	}
}

// Providers and routes are configuration, not secret material: they need a session and
// CSRF, and deliberately not the five-minute password window credential edits require.
func TestAIAdminNeedsASessionButNotReauth(t *testing.T) {
	h, _ := newCredsHarness(t, nil)
	if rec := h.do(http.MethodGet, "/api/admin/ai/providers", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", rec.Code)
	}

	h.bootstrapAdmin()
	h.reauth()
	h.createKey(t, "fake-key")

	pol, err := policy.New(h.store, h.store, h.server.deps.Events, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := ai.New(h.store, h.store, h.server.deps.Events, pol,
		h.server.deps.Credentials, ai.Options{Refs: h.server.deps.Credentials})
	if err != nil {
		t.Fatal(err)
	}
	h.server.deps.AI = svc

	// Expire the reauthentication window the way time would.
	if _, err := h.store.Exec(context.Background(), `UPDATE core_sessions SET reauth_at = NULL`); err != nil {
		t.Fatal(err)
	}
	if rec := h.do(http.MethodPut, "/api/admin/ai/providers/local", map[string]any{
		"kind": "fake", "credentialId": "fake-key", "billing": "metered",
	}); rec.Code != http.StatusNoContent {
		t.Fatalf("put provider without reauth: %d %s", rec.Code, rec.Body)
	}
	// The same session still may not touch a credential without reauthenticating.
	if rec := h.do(http.MethodDelete, "/api/admin/credentials/fake-key", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("credential delete without reauth: %d %s", rec.Code, rec.Body)
	}
}

func TestAIAssignUsesPluginDeclaredCapabilities(t *testing.T) {
	h, _ := newAIAdminHarness(t)
	h.createKey(t, "fake-key")

	reg, err := pluginhost.New(context.Background(), h.store, pluginhost.Options{
		DB: h.store, Policy: h.server.deps.Policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterAll(&modelPlugin{id: "fixture", models: []host.ModelNeed{{
		Name: "cheap-chat", Capabilities: []string{"chat"}, Purpose: "assignment fixture",
	}}}); err != nil {
		t.Fatal(err)
	}
	h.server.deps.PluginHost = reg

	if rec := h.do(http.MethodPut, "/api/admin/ai/providers/local", map[string]any{
		"kind": "fake", "credentialId": "fake-key", "billing": "metered",
	}); rec.Code != http.StatusNoContent {
		t.Fatalf("provider: %d %s", rec.Code, rec.Body)
	}

	rec := h.do(http.MethodGet, "/api/admin/plugins", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("plugins: %d %s", rec.Code, rec.Body)
	}
	var plugins struct {
		Data []pluginView `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &plugins); err != nil {
		t.Fatal(err)
	}
	if len(plugins.Data) != 1 || len(plugins.Data[0].Models) != 1 || plugins.Data[0].Models[0].Status != needMissing {
		t.Fatalf("before assign: %+v", plugins.Data)
	}

	if rec := h.do(http.MethodPut, "/api/admin/ai/routes/cheap-chat/assign", map[string]any{
		"provider": "local", "model": "echo",
	}); rec.Code != http.StatusNoContent {
		t.Fatalf("assign: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do(http.MethodPut, "/api/admin/ai/routes/cheap-vision/assign", map[string]any{
		"provider": "local", "model": "echo",
	}); rec.Code != http.StatusNotFound {
		t.Fatalf("undeclared route: %d %s", rec.Code, rec.Body)
	}

	rec = h.do(http.MethodGet, "/api/admin/plugins", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &plugins); err != nil {
		t.Fatal(err)
	}
	need := plugins.Data[0].Models[0]
	if need.Status != needReady || need.Provider != "local" || need.Model != "echo" {
		t.Fatalf("after assign: %+v", need)
	}

	rec = h.do(http.MethodGet, "/api/admin/ai/routes", nil)
	var routes struct {
		Data []ai.RouteDescriptor `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &routes); err != nil {
		t.Fatal(err)
	}
	if len(routes.Data) != 1 || routes.Data[0].LogicalName != "cheap-chat" || !routes.Data[0].Healthy {
		t.Fatalf("routes = %+v", routes.Data)
	}
	if len(routes.Data[0].Capabilities) != 1 || routes.Data[0].Capabilities[0] != "chat" {
		t.Fatalf("capabilities = %v", routes.Data[0].Capabilities)
	}
}
