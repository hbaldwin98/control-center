package web

import (
	"errors"
	"net/http"

	"github.com/hbaldwin98/control-center/internal/core/policy"
)

// The kill switch and budgets live under /api/admin/ rather than /api/plugins/, so a
// plugin's own HTTP namespace can never collide with host administration.

// handlePluginList returns every registered plugin's state, budget, and live spend.
func (s *Server) handlePluginList(w http.ResponseWriter, r *http.Request) {
	if s.deps.Policy == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "policy unavailable")
		return
	}
	states, err := s.deps.Policy.List(r.Context())
	if err != nil {
		s.fail(w, "list plugins", err)
		return
	}
	if states == nil {
		states = []policy.State{}
	}
	tail, _, err := s.eventBoundary(r.Context())
	if err != nil {
		s.fail(w, "event boundary", err)
		return
	}
	writeSnapshot(w, states, formatID(tail))
}

type disableRequest struct {
	Reason string `json:"reason"`
}

// handlePluginEnable turns a plugin on. An automated plugin without a finite daily budget
// is refused here, matching the guardrail the UI shows inline.
func (s *Server) handlePluginEnable(w http.ResponseWriter, r *http.Request) {
	if s.deps.Policy == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "policy unavailable")
		return
	}
	id := r.PathValue("id")
	err := s.deps.Policy.Enable(r.Context(), id, actorAdmin, "enabled from the plugins screen")
	s.writePolicyResult(w, "enable plugin", err)
}

// handlePluginDisable is the host-capability kill switch. It rejects new host-managed jobs,
// AI dispatches, event handlers, plugin HTTP requests, event publications, and storage and
// blob mutations, and cancels admitted contexts. Reads and logs remain available.
func (s *Server) handlePluginDisable(w http.ResponseWriter, r *http.Request) {
	if s.deps.Policy == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "policy unavailable")
		return
	}
	var req disableRequest
	if !decodeJSON(w, r, maxAuthBody, &req) {
		return
	}
	if req.Reason == "" {
		req.Reason = "disabled from the plugins screen"
	}
	err := s.deps.Policy.Disable(r.Context(), r.PathValue("id"), actorAdmin, req.Reason)
	s.writePolicyResult(w, "disable plugin", err)
}

// handlePluginBudget replaces a plugin's budget.
func (s *Server) handlePluginBudget(w http.ResponseWriter, r *http.Request) {
	if s.deps.Policy == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "policy unavailable")
		return
	}
	var b policy.Budget
	if !decodeJSON(w, r, maxAuthBody, &b) {
		return
	}
	err := s.deps.Policy.SetBudget(r.Context(), r.PathValue("id"), b)
	s.writePolicyResult(w, "set budget", err)
}

// actorAdmin is the single administrator principal, recorded on every transition.
const actorAdmin = "admin"

// writePolicyResult maps policy's domain errors onto the JSON error envelope. The frontend
// branches on the code, never the message.
func (s *Server) writePolicyResult(w http.ResponseWriter, op string, err error) {
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, policy.ErrUnknownPlugin):
		writeError(w, http.StatusNotFound, CodeNotFound, "no such plugin")
	case errors.Is(err, policy.ErrAutomatedNeedsBudget):
		writeError(w, http.StatusConflict, CodeConflict,
			"an automated plugin needs a finite daily budget")
	case errors.Is(err, policy.ErrBudgetBelowSpend):
		writeError(w, http.StatusConflict, CodeConflict,
			"that limit is below the spend already reserved or committed in this period")
	case errors.Is(err, policy.ErrNegativeAmount):
		writeError(w, http.StatusBadRequest, CodeBadRequest, "amounts must be non-negative")
	case errors.Is(err, policy.ErrInvalidPluginID):
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid plugin id")
	case errors.Is(err, policy.ErrInvalidExceed):
		writeError(w, http.StatusBadRequest, CodeBadRequest, "onExceed must be reject or disable")
	default:
		s.fail(w, op, err)
	}
}
