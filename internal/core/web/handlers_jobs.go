package web

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/hbaldwin98/control-center/internal/core/jobs"
)

func (s *Server) handleJobList(w http.ResponseWriter, r *http.Request) {
	if s.deps.Jobs == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "jobs unavailable")
		return
	}
	q := r.URL.Query()
	f := jobs.Filter{
		PluginID: q.Get("plugin"),
		Name:     q.Get("name"),
		State:    jobs.State(q.Get("state")),
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 500 {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "limit must be 1..500")
			return
		}
		f.Limit = n
	}
	found, err := s.deps.Jobs.List(r.Context(), f)
	if err != nil {
		s.fail(w, "list jobs", err)
		return
	}
	if found == nil {
		found = []jobs.Job{}
	}
	tail, _, err := s.eventBoundary(r.Context())
	if err != nil {
		s.fail(w, "event boundary", err)
		return
	}
	writeSnapshot(w, found, formatID(tail))
}

func (s *Server) handleJobGet(w http.ResponseWriter, r *http.Request) {
	if s.deps.Jobs == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "jobs unavailable")
		return
	}
	id, ok := parseJobID(w, r)
	if !ok {
		return
	}
	j, err := s.deps.Jobs.Get(r.Context(), id)
	if errors.Is(err, jobs.ErrUnknownJob) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such job")
		return
	}
	if err != nil {
		s.fail(w, "get job", err)
		return
	}
	tail, _, err := s.eventBoundary(r.Context())
	if err != nil {
		s.fail(w, "event boundary", err)
		return
	}
	writeSnapshot(w, j, formatID(tail))
}

func (s *Server) handleJobCancel(w http.ResponseWriter, r *http.Request) {
	if s.deps.Jobs == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "jobs unavailable")
		return
	}
	id, ok := parseJobID(w, r)
	if !ok {
		return
	}
	err := s.deps.Jobs.Cancel(r.Context(), id)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, jobs.ErrUnknownJob):
		writeError(w, http.StatusNotFound, CodeNotFound, "no such job")
	case errors.Is(err, jobs.ErrAlreadyTerminal):
		writeError(w, http.StatusConflict, CodeConflict, "job is already terminal")
	default:
		s.fail(w, "cancel job", err)
	}
}

func parseJobID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid job id")
		return 0, false
	}
	return id, true
}
