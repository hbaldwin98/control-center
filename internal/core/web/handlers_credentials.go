package web

import (
	"errors"
	"net/http"

	"github.com/hbaldwin98/control-center/internal/core/credentials"
)

func (s *Server) credsOrUnavailable(w http.ResponseWriter) *credentials.Store {
	if s.deps.Credentials == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "credentials unavailable")
		return nil
	}
	return s.deps.Credentials
}

func (s *Server) handleCredentialList(w http.ResponseWriter, r *http.Request) {
	store := s.credsOrUnavailable(w)
	if store == nil {
		return
	}
	list, err := store.List(r.Context())
	if err != nil {
		s.fail(w, "list credentials", err)
		return
	}
	tail, _, err := s.eventBoundary(r.Context())
	if err != nil {
		s.fail(w, "event boundary", err)
		return
	}
	writeSnapshot(w, list, formatID(tail))
}

func (s *Server) handleCredentialReferences(w http.ResponseWriter, r *http.Request) {
	store := s.credsOrUnavailable(w)
	if store == nil {
		return
	}
	refs, err := store.References(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeCredentialResult(w, "credential references", err)
		return
	}
	writeJSON(w, http.StatusOK, refs)
}

func (s *Server) handleOAuthProviders(w http.ResponseWriter, r *http.Request) {
	store := s.credsOrUnavailable(w)
	if store == nil {
		return
	}
	writeJSON(w, http.StatusOK, store.OAuthProviders())
}

type apiKeyBody struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Secret   string `json:"secret"`
}

type secretBody struct {
	Secret string `json:"secret"`
}

func (s *Server) handleCredentialCreate(w http.ResponseWriter, r *http.Request) {
	store := s.credsOrUnavailable(w)
	if store == nil {
		return
	}
	var body apiKeyBody
	if !decodeJSON(w, r, maxAuthBody, &body) {
		return
	}
	got, err := store.CreateAPIKey(r.Context(), credentials.APIKeyInput{
		ID: body.ID, Provider: body.Provider, Secret: credentials.SecretInput{Value: body.Secret},
	})
	if err != nil {
		s.writeCredentialResult(w, "create credential", err)
		return
	}
	writeJSON(w, http.StatusCreated, got)
}

func (s *Server) handleCredentialReplace(w http.ResponseWriter, r *http.Request) {
	store := s.credsOrUnavailable(w)
	if store == nil {
		return
	}
	var body secretBody
	if !decodeJSON(w, r, maxAuthBody, &body) {
		return
	}
	got, err := store.ReplaceAPIKey(r.Context(), r.PathValue("id"), credentials.SecretInput{Value: body.Secret})
	if err != nil {
		s.writeCredentialResult(w, "replace credential", err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (s *Server) handleCredentialRotate(w http.ResponseWriter, r *http.Request) {
	store := s.credsOrUnavailable(w)
	if store == nil {
		return
	}
	var body secretBody
	if !decodeJSON(w, r, maxAuthBody, &body) {
		return
	}
	got, err := store.RotateAPIKey(r.Context(), r.PathValue("id"), credentials.SecretInput{Value: body.Secret})
	if err != nil {
		s.writeCredentialResult(w, "rotate credential", err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (s *Server) handleCredentialDelete(w http.ResponseWriter, r *http.Request) {
	store := s.credsOrUnavailable(w)
	if store == nil {
		return
	}
	err := store.Delete(r.Context(), r.PathValue("id"))
	s.writeCredentialResult(w, "delete credential", err)
}

func (s *Server) handleOAuthBegin(w http.ResponseWriter, r *http.Request) {
	store := s.credsOrUnavailable(w)
	if store == nil {
		return
	}
	sess := sessionFrom(r.Context())
	if sess == nil {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "authentication required")
		return
	}
	authURL, _, err := store.BeginOAuth(r.Context(), credentials.OAuthStart{
		Provider:    r.PathValue("provider"),
		SessionID:   sess.ID,
		RedirectURI: s.oauthCallbackURI(r),
	})
	if err != nil {
		s.writeCredentialResult(w, "begin oauth", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"authUrl": authURL})
}

func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	store := s.credsOrUnavailable(w)
	if store == nil {
		return
	}
	sess := sessionFrom(r.Context())
	if sess == nil {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "authentication required")
		return
	}
	_, err := store.CompleteOAuth(r.Context(), credentials.OAuthCallback{
		SessionID:   sess.ID,
		RedirectURI: s.oauthCallbackURI(r),
		State:       r.URL.Query().Get("state"),
		Code:        r.URL.Query().Get("code"),
	})
	loc := "/settings?oauth=ok"
	if err != nil {
		loc = "/settings?oauth=error"
	}
	http.Redirect(w, r, loc, http.StatusSeeOther)
}

func (s *Server) oauthCallbackURI(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	// Host, not Origin: the OAuth callback is a top-level GET from the provider and
	// carries the provider's Origin, which is not the allowlisted redirect.
	return scheme + "://" + r.Host + "/api/admin/credentials/oauth/callback"
}

func (s *Server) writeCredentialResult(w http.ResponseWriter, op string, err error) {
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, credentials.ErrUnknownCredential):
		writeError(w, http.StatusNotFound, CodeNotFound, "no such credential")
	case errors.Is(err, credentials.ErrCredentialInUse):
		writeError(w, http.StatusConflict, CodeConflict, "credential is referenced; remove the references first")
	case errors.Is(err, credentials.ErrDuplicateID):
		writeError(w, http.StatusConflict, CodeConflict, "a credential with that id already exists")
	case errors.Is(err, credentials.ErrInvalidID), errors.Is(err, credentials.ErrInvalidSecret),
		errors.Is(err, credentials.ErrKindMismatch), errors.Is(err, credentials.ErrRedirect),
		errors.Is(err, credentials.ErrUnknownProvider), errors.Is(err, credentials.ErrOAuthState):
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
	default:
		s.fail(w, op, err)
	}
}
