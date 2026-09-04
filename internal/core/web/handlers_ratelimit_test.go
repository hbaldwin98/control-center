package web

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestLoginFloodDoesNotLockOutTheAdministrator is the regression this file exists for.
// The attempt limiter once counted per endpoint alone, so anyone who could reach the
// listener could hold the shared "login" counter above its ceiling forever and the
// administrator would never get past it again.
func TestLoginFloodDoesNotLockOutTheAdministrator(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin()

	// A stranger burns their own budget. A malformed body is enough: the limiter is
	// charged before the body is read, which is exactly what made this cheap to abuse.
	h.peer = "203.0.113.5:40000"
	var locked bool
	for i := 0; i < loginAttemptsPerPeer+1; i++ {
		if h.do(http.MethodPost, "/api/auth/login", "not an object").Code == http.StatusTooManyRequests {
			locked = true
			break
		}
	}
	if !locked {
		t.Fatalf("the flooding peer was never limited after %d attempts", loginAttemptsPerPeer+1)
	}

	// The administrator, on another address, is unaffected.
	h.peer = "198.51.100.7:40000"
	if rec := h.do(http.MethodPost, "/api/auth/login",
		map[string]string{"password": testPassword}); rec.Code != http.StatusOK {
		t.Fatalf("administrator locked out by another peer's flood: %d %s", rec.Code, rec.Body)
	}
}

func TestLimiterPerPeerCeiling(t *testing.T) {
	l := newAttemptLimiter(3, 100, time.Minute)
	l.now = func() time.Time { return time.Unix(0, 0) }

	for i := 1; i <= 3; i++ {
		if !l.allow("login", "203.0.113.5", false) {
			t.Fatalf("attempt %d was refused below the ceiling", i)
		}
	}
	if l.allow("login", "203.0.113.5", false) {
		t.Fatal("the peer was allowed past its ceiling")
	}
	// A different peer keeps its own budget.
	if !l.allow("login", "198.51.100.7", false) {
		t.Fatal("a second peer was charged for the first peer's attempts")
	}
	// So does a different endpoint.
	if !l.allow("reauth", "203.0.113.5", false) {
		t.Fatal("one endpoint's ceiling closed another")
	}
}

// TestLimiterEndpointCeilingExemptsLoopback covers the flood bound and the one carve-out
// in it: an operator at the console must be able to log in while the outside is being
// hammered, but is still held to the ordinary per-peer ceiling.
func TestLimiterEndpointCeilingExemptsLoopback(t *testing.T) {
	l := newAttemptLimiter(2, 4, time.Minute)
	l.now = func() time.Time { return time.Unix(0, 0) }

	// Four attempts from four addresses exhausts the endpoint as a whole.
	for _, peer := range []string{"203.0.113.1", "203.0.113.2", "203.0.113.3", "203.0.113.4"} {
		if !l.allow("login", peer, false) {
			t.Fatalf("%s was refused below the endpoint ceiling", peer)
		}
	}
	if l.allow("login", "203.0.113.9", false) {
		t.Fatal("a fifth address was allowed past the endpoint ceiling")
	}

	// Loopback still gets in, twice, and is then held to its own ceiling.
	for i := 1; i <= 2; i++ {
		if !l.allow("login", "127.0.0.1", true) {
			t.Fatalf("loopback attempt %d was refused during a flood", i)
		}
	}
	if l.allow("login", "127.0.0.1", true) {
		t.Fatal("loopback was allowed past its per-peer ceiling")
	}
}

// TestLimiterEndpointCeilingBoundsTheMap is why the endpoint ceiling is charged first:
// the per-peer map cannot grow past it however many addresses a flood rotates through.
func TestLimiterEndpointCeilingBoundsTheMap(t *testing.T) {
	l := newAttemptLimiter(10, 5, time.Minute)
	l.now = func() time.Time { return time.Unix(0, 0) }

	for i := 0; i < 500; i++ {
		l.allow("login", string(rune('a'+i%26))+string(rune('a'+i/26)), false)
	}
	if len(l.peers) > 5 {
		t.Fatalf("peers map grew to %d entries under a rotating flood, want at most 5", len(l.peers))
	}
}

func TestLimiterResetClearsOnlyThePeer(t *testing.T) {
	l := newAttemptLimiter(2, 1, time.Minute)
	l.now = func() time.Time { return time.Unix(0, 0) }

	l.allow("login", "203.0.113.1", false)
	l.allow("login", "203.0.113.1", false)
	if l.allow("login", "203.0.113.1", false) {
		t.Fatal("peer was allowed past its ceiling before the reset")
	}

	l.reset("login", "203.0.113.1")
	if !l.allow("login", "203.0.113.1", false) {
		t.Fatal("the peer's counter was not cleared")
	}

	// The shared ceiling of one admitted peer is untouched by that success, so a second
	// address still cannot get in. Clearing it would let an attacker reopen the gate by
	// waiting for the administrator to log in.
	if l.allow("login", "203.0.113.2", false) {
		t.Fatal("a success cleared the shared ceiling, which would reopen the flood")
	}
}

// TestLimiterFloodFromOnePeerSparesTheCeiling is the second half of the lockout fix. The
// shared ceiling is charged per newly seen peer rather than per request, so one address
// hammering the endpoint cannot drain the budget that everybody else needs.
func TestLimiterFloodFromOnePeerSparesTheCeiling(t *testing.T) {
	l := newAttemptLimiter(5, 2, time.Minute)
	l.now = func() time.Time { return time.Unix(0, 0) }

	for i := 0; i < 1000; i++ {
		l.allow("login", "203.0.113.5", false)
	}
	if !l.allow("login", "198.51.100.7", false) {
		t.Fatal("one peer's flood drained the shared ceiling")
	}
}

func TestLimiterWindowExpires(t *testing.T) {
	l := newAttemptLimiter(1, 1, time.Minute)
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }

	if !l.allow("login", "203.0.113.1", false) {
		t.Fatal("first attempt refused")
	}
	if l.allow("login", "203.0.113.1", false) {
		t.Fatal("second attempt allowed inside the window")
	}
	now = now.Add(2 * time.Minute)
	if !l.allow("login", "203.0.113.1", false) {
		t.Fatal("the window never expired")
	}
}

// TestPasswordVerifyIsBounded proves the semaphore around Argon2id is real. Without it,
// unauthenticated requests each allocate 64 MiB concurrently, which is an out-of-memory
// kill well before the per-minute ceilings are reached.
func TestPasswordVerifyIsBounded(t *testing.T) {
	h := newHarness(t)
	h.bootstrapAdmin()

	// Occupy every slot, so the next verification has to wait.
	for i := 0; i < verifyConcurrency; i++ {
		h.server.auth.verify <- struct{}{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := h.server.auth.checkPassword(ctx, testPassword)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("checkPassword ran with the semaphore full: err=%v", err)
	}

	// Releasing a slot lets the correct password through again.
	<-h.server.auth.verify
	if err := h.server.auth.checkPassword(context.Background(), testPassword); err != nil {
		t.Fatalf("checkPassword after release: %v", err)
	}
}

// TestPeerIP keeps the rate-limiting identity honest: the port must not split one
// client's attempts across buckets, and a forwarded header must never be believed.
func TestPeerIP(t *testing.T) {
	cases := map[string]string{
		"203.0.113.5:40000":   "203.0.113.5",
		"[2001:db8::1]:40000": "2001:db8::1",
		"127.0.0.1:1":         "127.0.0.1",
		"no-port-at-all":      "no-port-at-all",
	}
	for remote, want := range cases {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(nil))
		req.RemoteAddr = remote
		req.Header.Set("X-Forwarded-For", "10.0.0.1")
		if got := peerIP(req); got != want {
			t.Errorf("peerIP(%q) = %q, want %q", remote, got, want)
		}
	}
}
