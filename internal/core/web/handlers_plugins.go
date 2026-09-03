package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/hbaldwin98/control-center/internal/core/ai"
	"github.com/hbaldwin98/control-center/internal/core/pluginhost"
	"github.com/hbaldwin98/control-center/internal/core/policy"
)

// The kill switch and budgets live under /api/admin/ rather than /api/plugins/, so a
// plugin's own HTTP namespace can never collide with host administration.

type pluginView struct {
	policy.State
	Name         string            `json:"name,omitempty"`
	Description  string            `json:"description,omitempty"`
	Health       pluginhost.Health `json:"health"`
	Jobs         []string          `json:"jobs,omitempty"`
	Config       json.RawMessage   `json:"config,omitempty"`
	ConfigSchema json.RawMessage   `json:"configSchema,omitempty"`
	Models       []modelNeedView   `json:"models,omitempty"`
	Events       []eventSpecView   `json:"events,omitempty"`
}

// handlePluginList returns every registered plugin's state, budget, live spend, and
// the schema-backed config the Plugins screen renders.
func (s *Server) handlePluginList(w http.ResponseWriter, r *http.Request) {
	if s.deps.PluginHost != nil {
		var routes []ai.RouteDescriptor
		if s.deps.AI != nil {
			list, err := s.deps.AI.Routes(r.Context())
			if err != nil {
				s.fail(w, "list ai routes", err)
				return
			}
			routes = list
		}
		list := s.deps.PluginHost.List()
		views := make([]pluginView, 0, len(list))
		for _, d := range list {
			v := pluginView{
				State: d.State, Name: d.Manifest.Name, Description: d.Manifest.Description,
				Health: d.Health, Jobs: d.Jobs, ConfigSchema: d.Manifest.Config.Schema,
				Models: bindModelNeeds(d.Manifest.Models, routes),
				Events: bindEventSpecs(d.Manifest.ID, d.Manifest.Events),
			}
			if raw, err := s.deps.PluginHost.GetConfig(r.Context(), d.Manifest.ID); err == nil {
				v.Config = raw
			}
			views = append(views, v)
		}
		tail, _, err := s.eventBoundary(r.Context())
		if err != nil {
			s.fail(w, "event boundary", err)
			return
		}
		writeSnapshot(w, views, formatID(tail))
		return
	}
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
	var err error
	if s.deps.PluginHost != nil {
		err = s.deps.PluginHost.Enable(r.Context(), id, actorAdmin, "enabled from the plugins screen")
	} else {
		err = s.deps.Policy.Enable(r.Context(), id, actorAdmin, "enabled from the plugins screen")
	}
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
	id := r.PathValue("id")
	var err error
	if s.deps.PluginHost != nil {
		err = s.deps.PluginHost.Disable(r.Context(), id, actorAdmin, req.Reason)
	} else {
		err = s.deps.Policy.Disable(r.Context(), id, actorAdmin, req.Reason)
	}
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
	case errors.Is(err, pluginhost.ErrUnknownPlugin):
		writeError(w, http.StatusNotFound, CodeNotFound, "no such plugin")
	case errors.Is(err, pluginhost.ErrInvalidConfig):
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
	case errors.Is(err, pluginhost.ErrIncomplete):
		writeError(w, http.StatusConflict, CodeConflict, err.Error())
	default:
		s.fail(w, op, err)
	}
}

func (s *Server) handlePluginAPI(w http.ResponseWriter, r *http.Request) {
	if s.deps.PluginHost == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such plugin")
		return
	}
	s.deps.PluginHost.ServeHTTP(w, r)
}

func (s *Server) handlePluginConfigGet(w http.ResponseWriter, r *http.Request) {
	if s.deps.PluginHost == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such plugin")
		return
	}
	raw, err := s.deps.PluginHost.GetConfig(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writePolicyResult(w, "get plugin config", err)
		return
	}
	tail, _, err := s.eventBoundary(r.Context())
	if err != nil {
		s.fail(w, "event boundary", err)
		return
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		value = json.RawMessage(raw)
	}
	writeSnapshot(w, value, formatID(tail))
}

func (s *Server) handlePluginConfigPut(w http.ResponseWriter, r *http.Request) {
	if s.deps.PluginHost == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such plugin")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBody)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "malformed JSON body")
		return
	}
	if !json.Valid(raw) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "malformed JSON body")
		return
	}
	err = s.deps.PluginHost.UpdateConfig(r.Context(), r.PathValue("id"), json.RawMessage(raw), actorAdmin)
	s.writePolicyResult(w, "update plugin config", err)
}
