package web

import (
	"errors"
	"net/http"
	"strconv"

	harnesscore "github.com/hbaldwin98/control-center/internal/core/harness"
)

const maxHarnessBody = 40 << 10

type harnessList struct {
	Profiles []harnesscore.Profile `json:"profiles"`
	Sessions []harnesscore.Session `json:"sessions"`
}

func (s *Server) handleHarnessList(w http.ResponseWriter, r *http.Request) {
	if s.deps.Harness == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "harness unavailable")
		return
	}
	sessions, err := s.deps.Harness.List(r.Context(), 100)
	if err != nil {
		s.fail(w, "list harness sessions", err)
		return
	}
	tail, _, err := s.eventBoundary(r.Context())
	if err != nil {
		s.fail(w, "event boundary", err)
		return
	}
	writeSnapshot(w, harnessList{Profiles: s.deps.Harness.Profiles(), Sessions: sessions}, formatID(tail))
}

func (s *Server) handleHarnessCreate(w http.ResponseWriter, r *http.Request) {
	if s.deps.Harness == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "harness unavailable")
		return
	}
	var body struct {
		ProfileID   string `json:"profileId"`
		Title       string `json:"title"`
		Workspace   string `json:"workspace"`
		Instruction string `json:"instruction"`
	}
	if !decodeJSON(w, r, maxHarnessBody, &body) {
		return
	}
	session, err := s.deps.Harness.Create(r.Context(), harnesscore.CreateInput{
		ProfileID: body.ProfileID, Title: body.Title, Workspace: body.Workspace, Instruction: body.Instruction,
	})
	switch {
	case err == nil:
		writeJSON(w, http.StatusCreated, session)
	case errors.Is(err, harnesscore.ErrUnknownProfile), errors.Is(err, harnesscore.ErrInvalidInput), errors.Is(err, harnesscore.ErrWorkspaceNotAllowed):
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
	case errors.Is(err, harnesscore.ErrCapacity):
		writeError(w, http.StatusConflict, CodeConflict, "harness session capacity reached")
	case errors.Is(err, harnesscore.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "harness unavailable")
	default:
		s.fail(w, "create harness session", err)
	}
}

func (s *Server) handleHarnessGet(w http.ResponseWriter, r *http.Request) {
	if s.deps.Harness == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "harness unavailable")
		return
	}
	id, ok := parseHarnessID(w, r)
	if !ok {
		return
	}
	session, err := s.deps.Harness.Get(r.Context(), id)
	if errors.Is(err, harnesscore.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such harness session")
		return
	}
	if err != nil {
		s.fail(w, "get harness session", err)
		return
	}
	tail, _, err := s.eventBoundary(r.Context())
	if err != nil {
		s.fail(w, "event boundary", err)
		return
	}
	writeSnapshot(w, session, formatID(tail))
}

func (s *Server) handleHarnessStop(w http.ResponseWriter, r *http.Request) {
	if s.deps.Harness == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "harness unavailable")
		return
	}
	id, ok := parseHarnessID(w, r)
	if !ok {
		return
	}
	err := s.deps.Harness.Stop(r.Context(), id)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, harnesscore.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, "no such harness session")
	case errors.Is(err, harnesscore.ErrTerminal):
		writeError(w, http.StatusConflict, CodeConflict, "harness session is already terminal")
	default:
		s.fail(w, "stop harness session", err)
	}
}

func parseHarnessID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid harness session id")
		return 0, false
	}
	return id, true
}
