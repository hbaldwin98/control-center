package web

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	harnesscore "github.com/hbaldwin98/control-center/internal/core/harness"
)

func TestHarnessRoutesRequireAuthAndExposeSnapshot(t *testing.T) {
	h := newHarness(t)
	service, err := harnesscore.New(context.Background(), h.store, h.store, nil, harnesscore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	h.server.deps.Harness = service

	if rec := h.do(http.MethodGet, "/api/harness", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list = %d, want 401", rec.Code)
	}
	h.bootstrapAdmin()
	if rec := h.do(http.MethodGet, "/api/harness", nil); rec.Code != http.StatusOK {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	h.reauth()
	if rec := h.do(http.MethodPost, "/api/harness", map[string]string{"profileId": "missing"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown profile = %d %s", rec.Code, rec.Body.String())
	}
}

// TestHarnessCreateRequiresReauth matches the gate to the consequence: starting a session
// runs a program on this machine, so a session cookie on its own must not be enough.
// Reading and stopping stay behind the ordinary session, because the safe direction has
// to remain easy.
func TestHarnessCreateRequiresReauth(t *testing.T) {
	h := newHarness(t)
	service, err := harnesscore.New(context.Background(), h.store, h.store, nil, harnesscore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	h.server.deps.Harness = service
	h.bootstrapAdmin()

	rec := h.do(http.MethodPost, "/api/harness", map[string]string{"profileId": "missing"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("create without reauth = %d %s", rec.Code, rec.Body.String())
	}
	var body errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != CodeReauthRequired {
		t.Fatalf("error code = %q, want %q", body.Error.Code, CodeReauthRequired)
	}

	// Stopping does not need one, so an operator can always shut a session down.
	if rec := h.do(http.MethodPost, "/api/harness/1/stop", nil); rec.Code == http.StatusForbidden {
		t.Fatal("stopping a session was gated behind reauthentication")
	}

	h.reauth()
	if rec := h.do(http.MethodPost, "/api/harness", map[string]string{"profileId": "missing"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("create after reauth = %d %s", rec.Code, rec.Body.String())
	}
}

func TestHarnessMutationRequiresCSRF(t *testing.T) {
	h := newHarness(t)
	service, err := harnesscore.New(context.Background(), h.store, h.store, nil, harnesscore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	h.server.deps.Harness = service
	h.bootstrapAdmin()
	h.csrf = ""
	if rec := h.do(http.MethodPost, "/api/harness", map[string]string{"profileId": "missing"}); rec.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF = %d, want 403", rec.Code)
	}
}
