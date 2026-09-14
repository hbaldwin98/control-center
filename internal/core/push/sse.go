package push

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/policy"
)

// SSE is the first transport over the hub. It is deliberately thin: hold the
// response open, hand the hub's messages to the client as they arrive, and end when
// either side goes away. Everything that could be got wrong lives in the hub, so a
// second transport -- a websocket, when a client needs to send as well as receive --
// is another file this size and no change a plugin can observe.
const (
	// defaultHeartbeat keeps intermediaries from reaping an idle stream and lets the
	// client notice a connection that has silently died.
	defaultHeartbeat = 20 * time.Second
	// defaultWriteTimeout bounds one write. A client that cannot drain is closed; the
	// hub's own buffer already absorbs a brief stall.
	defaultWriteTimeout = 10 * time.Second
)

// ServeSSE streams a plugin's topics to one client over Server-Sent Events. The
// caller has already authenticated the request and resolved which plugin it names;
// the topics come from a repeated or comma-separated "topics" query parameter.
func (s *Service) ServeSSE(w http.ResponseWriter, r *http.Request, pluginID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}

	conn, err := s.Open(r.Context(), pluginID, topicsFromQuery(r))
	if err != nil {
		writeOpenError(w, err)
		return
	}
	defer conn.Close()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	// Defeat proxy buffering, which would otherwise hold messages until a buffer fills.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	rc := http.NewResponseController(w)
	send := func(chunk string) bool {
		if err := rc.SetWriteDeadline(time.Now().Add(s.opts.WriteTimeout)); err != nil {
			// Without a deadline one stuck client would pin this goroutine forever.
			return false
		}
		if _, err := w.Write([]byte(chunk)); err != nil {
			return false
		}
		flusher.Flush()
		// SetWriteDeadline is a connection-level deadline, not a per-call
		// timeout. Clear it after the flush or an idle stream expires when the
		// next heartbeat arrives after WriteTimeout.
		if err := rc.SetWriteDeadline(time.Time{}); err != nil {
			return false
		}
		return true
	}

	beat := time.NewTicker(s.opts.Heartbeat)
	defer beat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return

		case <-beat.C:
			if !send(": heartbeat\n\n") {
				return
			}

		case msg, open := <-conn.Messages():
			if !open {
				// The hub ended it: the plugin was disabled, or this client fell too
				// far behind. Either way the client reconnects and refetches.
				send(sseFrame("closed", "", "{}"))
				return
			}
			if !send(sseFrame(msg.Event, msg.Topic, string(msg.Data))) {
				return
			}
		}
	}
}

// sseFrame formats one Server-Sent Event. The topic rides in the frame body rather
// than the event name so a client registers one listener per kind, not per topic.
func sseFrame(event, topic, data string) string {
	if data == "" {
		data = "{}"
	}
	var b strings.Builder
	b.WriteString("event: ")
	b.WriteString(event)
	b.WriteString("\ndata: {\"topic\":")
	b.WriteString(strconv.Quote(topic))
	b.WriteString(",\"data\":")
	// Payloads are compact JSON from the hub, so they never contain a raw newline.
	b.WriteString(data)
	b.WriteString("}\n\n")
	return b.String()
}

// topicsFromQuery reads the watch set, accepting both a repeated parameter and one
// comma-separated value so a long list stays inside a sane URL.
func topicsFromQuery(r *http.Request) []string {
	var out []string
	for _, raw := range r.URL.Query()["topics"] {
		for _, name := range strings.Split(raw, ",") {
			if name = strings.TrimSpace(name); name != "" {
				out = append(out, name)
			}
		}
	}
	return out
}

// writeOpenError maps a refusal to a status the client can act on.
func writeOpenError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, policy.ErrPluginDisabled):
		writeError(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
	case errors.Is(err, ErrInvalidTopic):
		writeError(w, http.StatusBadRequest, "bad_request", "invalid topic")
	case errors.Is(err, ErrLimit):
		writeError(w, http.StatusTooManyRequests, "limit", "too many live connections")
	case errors.Is(err, ErrNoPlugin):
		writeError(w, http.StatusNotFound, "not_found", "unknown plugin")
	default:
		writeError(w, http.StatusServiceUnavailable, "internal", "live delivery unavailable")
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":{"code":"` + code + `","message":"` + message + `"}}`))
}
