package web

import (
	"net/http"
)

// Live delivery is mounted here rather than under /api/plugins/ because the transport
// is the host's, not the plugin's. One authenticated endpoint means one place where
// connection limits, framing, and teardown are decided, and a plugin that wants a live
// screen writes no transport code at all.
func (s *Server) handlePush(w http.ResponseWriter, r *http.Request) {
	if s.deps.Push == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "live delivery unavailable")
		return
	}
	id := r.PathValue("plugin")
	// A plugin that is not registered must not be able to hold a connection, or an
	// unknown id would be a free way to occupy one.
	if !s.pluginRegistered(r, id) {
		writeError(w, http.StatusNotFound, CodeNotFound, "unknown plugin")
		return
	}
	s.deps.Push.ServeSSE(w, r, id)
}

// pluginRegistered reports whether the id names a plugin this install registered.
func (s *Server) pluginRegistered(r *http.Request, id string) bool {
	if id == "" || s.deps.Plugins == nil {
		return false
	}
	for _, p := range s.deps.Plugins(r.Context()) {
		if p.ID == id {
			return true
		}
	}
	return false
}
