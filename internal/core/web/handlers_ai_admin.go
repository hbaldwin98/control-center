package web

import (
	"errors"
	"net/http"

	"github.com/hbaldwin98/control-center/internal/core/ai"
)

// maxRouteBody is larger than maxAuthBody because a route carries an attempt plan.
const maxRouteBody = 32 << 10

func (s *Server) aiOrUnavailable(w http.ResponseWriter) *ai.Service {
	if s.deps.AI == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "ai unavailable")
		return nil
	}
	return s.deps.AI
}

func (s *Server) handleAIProviderList(w http.ResponseWriter, r *http.Request) {
	svc := s.aiOrUnavailable(w)
	if svc == nil {
		return
	}
	list, err := svc.Providers(r.Context())
	if err != nil {
		s.fail(w, "list ai providers", err)
		return
	}
	tail, _, err := s.eventBoundary(r.Context())
	if err != nil {
		s.fail(w, "event boundary", err)
		return
	}
	writeSnapshot(w, list, formatID(tail))
}

func (s *Server) handleAIProviderPut(w http.ResponseWriter, r *http.Request) {
	svc := s.aiOrUnavailable(w)
	if svc == nil {
		return
	}
	var body ai.ProviderConfig
	if !decodeJSON(w, r, maxAuthBody, &body) {
		return
	}
	// The path owns the identity; a mismatched body field is a client bug, not a rename.
	body.ID = r.PathValue("id")
	if err := svc.PutProvider(r.Context(), body); err != nil {
		s.writeAIResult(w, "put ai provider", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAIProviderDelete(w http.ResponseWriter, r *http.Request) {
	svc := s.aiOrUnavailable(w)
	if svc == nil {
		return
	}
	s.writeAIResult(w, "delete ai provider", svc.DeleteProvider(r.Context(), r.PathValue("id")))
}

// handleAIModels serves a provider's model catalog. It reads the cache unless
// ?refresh=1 is asked for, so opening Settings does not call every provider.
func (s *Server) handleAIModels(w http.ResponseWriter, r *http.Request) {
	svc := s.aiOrUnavailable(w)
	if svc == nil {
		return
	}
	refresh := r.URL.Query().Get("refresh") == "1"
	models, err := svc.Models(r.Context(), r.PathValue("id"), refresh)
	if err != nil {
		s.writeAIResult(w, "list ai models", err)
		return
	}
	if models == nil {
		models = []ai.Model{}
	}
	writeJSON(w, http.StatusOK, models)
}

func (s *Server) handleAIRoutePut(w http.ResponseWriter, r *http.Request) {
	svc := s.aiOrUnavailable(w)
	if svc == nil {
		return
	}
	var body ai.RouteInput
	if !decodeJSON(w, r, maxRouteBody, &body) {
		return
	}
	body.Name = r.PathValue("name")
	if err := svc.PutRoute(r.Context(), body); err != nil {
		s.writeAIResult(w, "put ai route", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAIRouteDelete(w http.ResponseWriter, r *http.Request) {
	svc := s.aiOrUnavailable(w)
	if svc == nil {
		return
	}
	s.writeAIResult(w, "delete ai route", svc.DeleteRoute(r.Context(), r.PathValue("name")))
}

func (s *Server) writeAIResult(w http.ResponseWriter, op string, err error) {
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ai.ErrUnknownProvider):
		writeError(w, http.StatusNotFound, CodeNotFound, "no such provider")
	case errors.Is(err, ai.ErrProviderInUse):
		writeError(w, http.StatusConflict, CodeConflict, err.Error())
	case errors.Is(err, ai.ErrInvalidProvider), errors.Is(err, ai.ErrInvalidRoute),
		errors.Is(err, ai.ErrMissingPrice), errors.Is(err, ai.ErrMissingCredential),
		errors.Is(err, ai.ErrSubscriptionOnly), errors.Is(err, ai.ErrUnbounded):
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
	case errors.Is(err, ai.ErrDiscovery), errors.Is(err, ai.ErrNotDiscoverable):
		// The provider itself failed or cannot be asked. That is not this server's
		// error, and it is the administrator who has to act on it.
		writeError(w, http.StatusBadGateway, CodeBadRequest, err.Error())
	default:
		s.fail(w, op, err)
	}
}
