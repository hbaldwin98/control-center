package web

import (
	"context"
	"net/http"
	"testing"
)

func TestChangePassword(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin()

	const newPassword = "a much better passphrase"

	// A second device is signed in at the same time. Changing the password must end it.
	otherCookie, _, err := h.server.auth.create(context.Background(), h.server.deps.Config.Session.Absolute)
	if err != nil {
		t.Fatalf("create second session: %v", err)
	}

	if rec := h.do(http.MethodPost, "/api/auth/password",
		map[string]string{"currentPassword": testPassword, "newPassword": "short"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("short password: got %d, want 400", rec.Code)
	}
	if rec := h.do(http.MethodPost, "/api/auth/password",
		map[string]string{"currentPassword": testPassword, "newPassword": testPassword}); rec.Code != http.StatusBadRequest {
		t.Fatalf("unchanged password: got %d, want 400", rec.Code)
	}
	if rec := h.do(http.MethodPost, "/api/auth/password",
		map[string]string{"currentPassword": "nope", "newPassword": newPassword}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong current password: got %d, want 401", rec.Code)
	}

	if rec := h.do(http.MethodPost, "/api/auth/password",
		map[string]string{"currentPassword": testPassword, "newPassword": newPassword}); rec.Code != http.StatusNoContent {
		t.Fatalf("change password: %d %s", rec.Code, rec.Body)
	}

	// The changing session survives; the other one is gone.
	if rec := h.do(http.MethodGet, "/api/bootstrap", nil); rec.Code != http.StatusOK {
		t.Fatalf("session after change: got %d, want 200", rec.Code)
	}
	if _, err := h.server.auth.lookup(context.Background(), otherCookie, h.server.deps.Config.Session.Idle); err == nil {
		t.Fatal("the other session survived the password change")
	}

	// Only the new password authenticates.
	if rec := h.do(http.MethodPost, "/api/auth/login",
		map[string]string{"password": testPassword}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old password: got %d, want 401", rec.Code)
	}
	if rec := h.do(http.MethodPost, "/api/auth/login",
		map[string]string{"password": newPassword}); rec.Code != http.StatusOK {
		t.Fatalf("new password: %d %s", rec.Code, rec.Body)
	}
}

func TestChangePasswordRequiresSession(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin()
	h.cookie = ""

	if rec := h.do(http.MethodPost, "/api/auth/password",
		map[string]string{"currentPassword": testPassword, "newPassword": "a much better passphrase"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: got %d, want 401", rec.Code)
	}
}
