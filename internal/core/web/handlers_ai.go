package web

import (
	"net/http"
	"strconv"

	"github.com/hbaldwin98/control-center/internal/core/ai"
)

func (s *Server) handleAIRoutes(w http.ResponseWriter, r *http.Request) {
	if s.deps.AI == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "ai unavailable")
		return
	}
	routes, err := s.deps.AI.Routes(r.Context())
	if err != nil {
		s.fail(w, "list ai routes", err)
		return
	}
	if routes == nil {
		routes = []ai.RouteDescriptor{}
	}
	tail, _, err := s.eventBoundary(r.Context())
	if err != nil {
		s.fail(w, "event boundary", err)
		return
	}
	writeSnapshot(w, routes, formatID(tail))
}

func (s *Server) handleAICalls(w http.ResponseWriter, r *http.Request) {
	if s.deps.AI == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "ai unavailable")
		return
	}
	q := r.URL.Query()
	cq := ai.CallQuery{
		PluginID:     q.Get("plugin"),
		JobID:        q.Get("job"),
		LogicalModel: q.Get("model"),
		Status:       q.Get("status"),
		AfterID:      q.Get("after"),
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "limit must be 1..200")
			return
		}
		cq.Limit = n
	}
	page, err := s.deps.AI.Calls(r.Context(), cq)
	if err != nil {
		s.fail(w, "list ai calls", err)
		return
	}
	tail, _, err := s.eventBoundary(r.Context())
	if err != nil {
		s.fail(w, "event boundary", err)
		return
	}
	writeSnapshot(w, page, formatID(tail))
}
