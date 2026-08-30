package web

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/storage"
)

// PluginDescriptor is the authenticated backend view of a registered plugin. Before
// rendering any plugin UI the shell compares these ids with its compiled-in modules and
// fails closed on an unknown, missing, or duplicate id.
type PluginDescriptor struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// shellBootstrap is the initial core state the shell loads before opening the one shared
// event stream. Later milestones extend it; the envelope and the asOfEventId boundary do
// not change.
type shellBootstrap struct {
	CSRFToken        string             `json:"csrfToken"`
	ServerTime       time.Time          `json:"serverTime"`
	Session          sessionInfo        `json:"session"`
	Plugins          []PluginDescriptor `json:"plugins"`
	OldestRetainedID string             `json:"oldestRetainedId"`
}

type sessionInfo struct {
	ExpiresAt time.Time  `json:"expiresAt"`
	ReauthAt  *time.Time `json:"reauthAt"`
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

	tail, oldest, err := s.eventBoundary(r.Context())
	if err != nil {
		s.fail(w, "event boundary", err)
		return
	}

	writeSnapshot(w, shellBootstrap{
		CSRFToken:  csrf,
		ServerTime: s.now().UTC(),
		Session: sessionInfo{
			ExpiresAt: sess.ExpiresAt,
			ReauthAt:  sess.ReauthAt,
		},
		Plugins:          s.pluginDescriptors(r.Context()),
		OldestRetainedID: formatID(oldest),
	}, formatID(tail))
}

// eventBoundary reads the log tail and the retention boundary in one transaction, so the
// snapshot and the stream position a client opens after it cannot disagree.
func (s *Server) eventBoundary(ctx context.Context) (tail, oldest int64, err error) {
	if s.deps.Events == nil {
		return 0, 1, nil
	}
	err = s.deps.DB.Tx(ctx, func(tx storage.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(id), 0) FROM core_events`).Scan(&tail); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`SELECT oldest_retained_id FROM core_event_retention WHERE id = 1`).Scan(&oldest)
	})
	return tail, oldest, err
}

// pluginDescriptors reports the registered plugins the shell reconciles against.
// pluginhost supplies them at milestone 6; until then the registry is empty so the
// shell reconciles against nothing. Policy state is a different list: the Plugins
// screen reads it from /api/admin/plugins, not from bootstrap. Mixing the two would
// fail the shell closed as soon as a plugin was registered without a frontend module.
func (s *Server) pluginDescriptors(ctx context.Context) []PluginDescriptor {
	if s.deps.Plugins == nil {
		return []PluginDescriptor{}
	}
	out := s.deps.Plugins(ctx)
	if out == nil {
		return []PluginDescriptor{}
	}
	return out
}

// fail logs the underlying cause and returns an opaque error to the client.
func (s *Server) fail(w http.ResponseWriter, op string, err error) {
	slog.Error("web: "+op, "err", err)
	writeError(w, http.StatusInternalServerError, CodeInternal, "internal error")
}
