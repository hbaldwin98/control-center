package web

import (
	"context"
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
	if rec := h.do(http.MethodPost, "/api/harness", map[string]string{"profileId": "missing"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown profile = %d %s", rec.Code, rec.Body.String())
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
