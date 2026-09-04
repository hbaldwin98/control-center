package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/hbaldwin98/control-center/internal/core/events"
)

// authEvents reads the log for one type, so a test can assert on what an administrator
// would actually receive rather than on a log line nobody reads.
func authEvents(t *testing.T, bus *events.Log, typ string) []events.Event {
	t.Helper()
	out, err := bus.Query(context.Background(), events.Query{Pattern: typ, Limit: 100})
	if err != nil {
		t.Fatalf("query %s: %v", typ, err)
	}
	return out
}

// TestFailedLoginIsReported is finding 6 of the exposure audit: a rejected attempt used
// to leave no trace at all, so an administrator being brute-forced could not tell that
// from having forgotten their own password.
func TestFailedLoginIsReported(t *testing.T) {
	h, bus := newEventsHarness(t)
	h.bootstrapAdmin()
	h.peer = "203.0.113.5:40000"

	if rec := h.do(http.MethodPost, "/api/auth/login",
		map[string]string{"password": "not the password"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("login: got %d, want 401", rec.Code)
	}

	got := authEvents(t, bus, events.TypeAuthFailed)
	if len(got) != 1 {
		t.Fatalf("got %d failure events, want 1", len(got))
	}
	if got[0].Source != events.SourceWeb || got[0].Subject != "login" {
		t.Errorf("event = source %q subject %q", got[0].Source, got[0].Subject)
	}

	// The address is carried so the administrator can tell one attacker from many. The
	// submitted password must never appear, in any form.
	payload := string(got[0].Payload)
	if !strings.Contains(payload, "203.0.113.5") {
		t.Errorf("payload does not name the peer: %s", payload)
	}
	if strings.Contains(payload, "not the password") {
		t.Fatalf("the submitted password was recorded: %s", payload)
	}
}

// TestLockoutIsReportedSeparately keeps the two signals apart: a wrong password and a
// peer that has spent its budget mean different things to whoever reads the inbox.
func TestLockoutIsReportedSeparately(t *testing.T) {
	h, bus := newEventsHarness(t)
	h.bootstrapAdmin()
	h.peer = "203.0.113.5:40000"

	var locked bool
	for i := 0; i < loginAttemptsPerPeer+1; i++ {
		if h.do(http.MethodPost, "/api/auth/login", "not an object").Code == http.StatusTooManyRequests {
			locked = true
			break
		}
	}
	if !locked {
		t.Fatal("the peer was never rate limited")
	}

	if got := authEvents(t, bus, events.TypeAuthLockedOut); len(got) == 0 {
		t.Fatal("a lockout produced no event")
	} else if got[0].Subject != "login" {
		t.Errorf("lockout subject = %q, want login", got[0].Subject)
	}
	// A malformed body never reaches the password check, so nothing should claim a
	// credential was rejected.
	if got := authEvents(t, bus, events.TypeAuthFailed); len(got) != 0 {
		t.Errorf("got %d credential-failure events for malformed bodies, want 0", len(got))
	}
}

func TestSuccessfulLoginIsNotReportedAsFailure(t *testing.T) {
	h, bus := newEventsHarness(t)
	h.bootstrapAdmin()

	if rec := h.do(http.MethodPost, "/api/auth/login",
		map[string]string{"password": testPassword}); rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	if got := authEvents(t, bus, events.TypeAuthFailed); len(got) != 0 {
		t.Fatalf("a successful login produced %d failure events", len(got))
	}
}
