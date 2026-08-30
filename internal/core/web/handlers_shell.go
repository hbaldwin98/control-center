package web

import (
	"log/slog"
	"net/http"
	"time"
)

// shellBootstrap is the initial core state the shell loads before opening the one shared
// event stream. Later milestones extend Data; the envelope and the asOfEventId boundary
// do not change.
type shellBootstrap struct {
	CSRFToken  string       `json:"csrfToken"`
	ServerTime time.Time    `json:"serverTime"`
	Session    sessionInfo  `json:"session"`
	Plugins    []pluginDesc `json:"plugins"`
}

type sessionInfo struct {
	ExpiresAt time.Time  `json:"expiresAt"`
	ReauthAt  *time.Time `json:"reauthAt"`
}

// pluginDesc is the authenticated backend descriptor the shell compares against its
// compiled-in PluginModule ids, failing closed on any mismatch.
type pluginDesc struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// handleShellBootstrap returns the initial snapshot. The data and the event-log tail come
// from one read transaction so the client can apply buffered stream events strictly above
// asOfEventId without a gap or a duplicate.
func (s *Server) handleShellBootstrap(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())

	// A fresh synchronizer token per bootstrap keeps a reloaded tab able to mutate
	// without another login, and retires the previous token.
	csrf, err := s.auth.rotateCSRF(r.Context(), sess.ID)
	if err != nil {
		s.fail(w, "rotate csrf", err)
		return
	}

	// The event log arrives in milestone 2; until then the boundary is the empty log.
	tail := int64(0)

	writeSnapshot(w, shellBootstrap{
		CSRFToken:  csrf,
		ServerTime: s.now().UTC(),
		Session: sessionInfo{
			ExpiresAt: sess.ExpiresAt,
			ReauthAt:  sess.ReauthAt,
		},
		Plugins: []pluginDesc{},
	}, formatID(tail))
}

// fail logs the underlying cause and returns an opaque error to the client.
func (s *Server) fail(w http.ResponseWriter, op string, err error) {
	slog.Error("web: "+op, "err", err)
	writeError(w, http.StatusInternalServerError, CodeInternal, "internal error")
}
