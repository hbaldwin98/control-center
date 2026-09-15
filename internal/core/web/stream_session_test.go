package web

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestStreamHeartbeatKeepsTheAuthenticatedSessionAlive covers the browser behavior that
// matters here: the shell has one long-lived authenticated SSE request and may make no REST
// requests while a screen is open. A heartbeat is activity for that connection, not an idle
// session.
func TestStreamHeartbeatKeepsTheAuthenticatedSessionAlive(t *testing.T) {
	oldHeartbeat := heartbeatInterval
	heartbeatInterval = 5 * time.Millisecond
	t.Cleanup(func() { heartbeatInterval = oldHeartbeat })

	h := newLiveHarness(t)
	h.harness.server.deps.Config.Session.Idle = 15 * time.Millisecond
	cookie := h.login(t)
	body, closeStream := h.openStream(t, cookie, "", "")
	defer closeStream()

	// Read several actual heartbeat frames so this test observes the stream's keepalive
	// path rather than relying on a scheduler sleep to have happened.
	for range 6 {
		readHeartbeat(t, body)
	}

	if _, err := h.harness.server.auth.lookup(context.Background(), cookie.Value, h.harness.server.deps.Config.Session.Idle); err != nil {
		t.Fatalf("session expired while its SSE stream was receiving heartbeats: %v", err)
	}
}

func readHeartbeat(t *testing.T, body interface{ ReadString(byte) (string, error) }) {
	t.Helper()
	for {
		line, err := body.ReadString('\n')
		if err != nil {
			t.Fatalf("read heartbeat: %v", err)
		}
		if strings.TrimRight(line, "\r\n") == "" {
			return
		}
	}
}
