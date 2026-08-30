package web

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/events"
)

// One authenticated SSE stream carries every authorized committed event. The shell opens
// it once and multiplexes dot-segment patterns locally; clients never send server-side
// subscription patterns.
const (
	// heartbeatInterval keeps intermediaries and stale-connection detection working.
	heartbeatInterval = 20 * time.Second

	// writeTimeout bounds one write to a client. A client too slow to drain the socket
	// has its stream closed; the reconnect replays from the last delivered ID instead of
	// silently dropping an event.
	writeTimeout = 10 * time.Second

	// streamBatch bounds how many events one read cycle sends.
	streamBatch = 200
)

// wireEvent is the on-the-wire envelope. IDs are decimal strings because an int64 event ID
// does not survive a JavaScript Number.
type wireEvent struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Source    string          `json:"source"`
	Subject   string          `json:"subject"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"createdAt"`
}

func toWire(e events.Event) wireEvent {
	payload := e.Payload
	if len(payload) == 0 {
		payload = json.RawMessage("null")
	}
	return wireEvent{
		ID:        formatID(e.ID),
		Type:      e.Type,
		Source:    e.Source,
		Subject:   e.Subject,
		Payload:   payload,
		CreatedAt: e.CreatedAt,
	}
}

// handleStream tails core_events directly rather than a lossy live-subscription queue, so
// a queue overflow cannot silently create a client gap.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	if s.deps.Events == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "event stream unavailable")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, CodeInternal, "streaming unsupported")
		return
	}

	after, err := resumePoint(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid resume point")
		return
	}

	ctx := r.Context()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	// Defeat proxy buffering, which would otherwise hold events until a buffer fills.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	rc := http.NewResponseController(w)
	send := func(chunk string) bool {
		if err := rc.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
			// A server that cannot set a deadline would let one slow client wedge a
			// writer goroutine indefinitely, so close instead.
			return false
		}
		if _, err := w.Write([]byte(chunk)); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// A resume point below the retained boundary cannot be replayed, so tell the client
	// to reload rather than handing it a partial history.
	oldest, err := s.deps.Events.OldestRetainedID(ctx)
	if err != nil {
		s.fail(w, "oldest retained id", err)
		return
	}
	if after+1 < oldest {
		if !send(sseMessage("reset", "", fmt.Sprintf(`{"oldestRetainedId":%q}`, formatID(oldest)))) {
			return
		}
		after = oldest - 1
	}

	woken, unwatch := s.deps.Events.Watch()
	defer unwatch()

	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()
	// A slow fallback tick covers a missed wake-up; delivery never depends on it.
	poll := time.NewTicker(time.Second)
	defer poll.Stop()

	for {
		batch, err := s.deps.Events.Query(ctx, events.Query{
			Pattern: "**", AfterID: after, Limit: streamBatch,
		})
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("web: stream read failed", "err", err)
			}
			return
		}
		for _, e := range batch {
			body, err := json.Marshal(toWire(e))
			if err != nil {
				slog.Error("web: stream marshal failed", "event", e.ID, "err", err)
				return
			}
			if !send(sseMessage("event", formatID(e.ID), string(body))) {
				return
			}
			after = e.ID
		}
		if len(batch) == streamBatch {
			// More is waiting; read again before blocking.
			continue
		}

		select {
		case <-ctx.Done():
			return
		case <-woken:
		case <-poll.C:
		case <-heartbeat.C:
			if !send(": heartbeat\n\n") {
				return
			}
		}
	}
}

// sseMessage formats one Server-Sent Event frame.
func sseMessage(event, id, data string) string {
	var b strings.Builder
	if id != "" {
		b.WriteString("id: ")
		b.WriteString(id)
		b.WriteByte('\n')
	}
	b.WriteString("event: ")
	b.WriteString(event)
	b.WriteByte('\n')
	// The payload is compact JSON, so it never contains a raw newline.
	b.WriteString("data: ")
	b.WriteString(data)
	b.WriteString("\n\n")
	return b.String()
}

// resumePoint reads the client's position: the standard Last-Event-ID header a browser
// sends on automatic reconnect, or ?after=<id> for a newly constructed client.
func resumePoint(r *http.Request) (int64, error) {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("after")
	}
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 0 {
		return 0, fmt.Errorf("web: invalid resume point %q", raw)
	}
	return id, nil
}
