package web

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/hbaldwin98/control-center/internal/core/events"
)

// eventsPage is the Events screen's snapshot: a window of the persisted log plus the
// retention boundary, so the client knows when its position is unreplayable.
type eventsPage struct {
	Events           []wireEvent `json:"events"`
	OldestRetainedID string      `json:"oldestRetainedId"`
}

// handleEventsQuery serves the filterable log view. REST provides the initial snapshot;
// SSE applies later changes.
func (s *Server) handleEventsQuery(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	pattern := q.Get("pattern")
	if pattern == "" {
		pattern = "**"
	}
	if _, err := events.CompilePattern(pattern); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid pattern")
		return
	}

	var afterID int64
	if raw := q.Get("after"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id < 0 {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid after id")
			return
		}
		afterID = id
	}
	limit := 100
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 1000 {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "limit must be 1..1000")
			return
		}
		limit = n
	}

	found, err := s.deps.Events.Query(r.Context(), events.Query{
		Pattern: pattern, AfterID: afterID, Limit: limit,
	})
	if err != nil {
		s.fail(w, "query events", err)
		return
	}
	oldest, err := s.deps.Events.OldestRetainedID(r.Context())
	if err != nil {
		s.fail(w, "oldest retained id", err)
		return
	}
	tail, err := s.deps.Events.Tail(r.Context())
	if err != nil {
		s.fail(w, "event tail", err)
		return
	}

	page := eventsPage{Events: make([]wireEvent, 0, len(found)), OldestRetainedID: formatID(oldest)}
	for _, e := range found {
		page.Events = append(page.Events, toWire(e))
	}
	writeSnapshot(w, page, formatID(tail))
}

// handleSubscribers reports every durable cursor, including a paused one and why.
func (s *Server) handleSubscribers(w http.ResponseWriter, r *http.Request) {
	subs, err := s.deps.Events.Subscribers(r.Context())
	if err != nil {
		s.fail(w, "list subscribers", err)
		return
	}
	if subs == nil {
		subs = []events.SubscriberStatus{}
	}
	tail, err := s.deps.Events.Tail(r.Context())
	if err != nil {
		s.fail(w, "event tail", err)
		return
	}
	writeSnapshot(w, subs, formatID(tail))
}

// handleSubscriberAction performs the operator's choice on a paused subscriber: retry the
// poison event, skip it, or reset the cursor. Resetting is deliberately explicit; it also
// releases the retention pin the old position held.
func (s *Server) handleSubscriberAction(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	action := r.PathValue("action")

	var err error
	switch action {
	case "retry":
		err = s.deps.Events.RetryPaused(r.Context(), name)
	case "skip":
		err = s.deps.Events.SkipPaused(r.Context(), name)
	case "reset":
		var req struct {
			Mode    string `json:"mode"`
			EventID string `json:"eventId"`
		}
		if !decodeJSON(w, r, maxAuthBody, &req) {
			return
		}
		start := events.CursorStart{Mode: events.StartMode(req.Mode)}
		if start.Mode == events.AfterEvent {
			id, convErr := strconv.ParseInt(req.EventID, 10, 64)
			if convErr != nil || id < 0 {
				writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid eventId")
				return
			}
			start.EventID = id
		}
		err = s.deps.Events.ResetCursor(r.Context(), name, start)
	default:
		writeError(w, http.StatusNotFound, CodeNotFound, "no such action")
		return
	}

	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, events.ErrUnknownName):
		writeError(w, http.StatusNotFound, CodeNotFound, "no such subscriber")
	case errors.Is(err, events.ErrNotPaused):
		writeError(w, http.StatusConflict, CodeConflict, "subscriber is not paused")
	default:
		s.fail(w, "subscriber action", err)
	}
}
