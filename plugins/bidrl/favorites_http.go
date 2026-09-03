package bidrl

import (
	"net/http"
	"sort"
	"strings"
	"time"
)

// Saving a lot, and the note you leave on it.

type favoriteBody struct {
	Note string `json:"note"`
}

const maxFavoriteNote = 500

// handleFavorite saves a lot, or updates the note on one already saved. A direct
// write rather than a job: it is instant and local, and the rule that every button
// queues a job exists for work that takes time.
func (p *Plugin) handleFavorite(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	id := r.PathValue("id")
	var body favoriteBody
	if r.ContentLength > 0 && !decodeJSON(w, r, &body) {
		return
	}
	note := truncateRunes(collapseText(body.Note), maxFavoriteNote)
	var exists string
	if err := h.Store().QueryRow(r.Context(), `SELECT id FROM bidrl_lots WHERE id = ?`, id).Scan(&exists); err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "lot not found")
		return
	}
	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	if _, err := h.Store().Exec(r.Context(), `INSERT INTO bidrl_favorites(lot_id, note, created_at)
		VALUES (?, ?, ?)
		ON CONFLICT(lot_id) DO UPDATE SET note = excluded.note`, id, note, now); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lotId": id, "favorite": true, "note": note})
}

func (p *Plugin) handleUnfavorite(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	id := r.PathValue("id")
	if _, err := h.Store().Exec(r.Context(), `DELETE FROM bidrl_favorites WHERE lot_id = ?`, id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lotId": id, "favorite": false})
}

// handleListFavorites serves saved lots, newest save first. Location and category
// narrow it server-side the way they do on the catalog; sorting and the rest of the
// filtering is the catalog screen's own client-side machinery.
func (p *Plugin) handleListFavorites(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	where := `f.lot_id IS NOT NULL`
	var args []any
	if category := r.URL.Query().Get("category"); category != "" && category != "all" {
		where += ` AND IFNULL(NULLIF(l.category, ''), a.category) = ?`
		args = append(args, category)
	}
	if clause, vals := affiliateClause(r.URL.Query()); clause != "" {
		where += clause
		args = append(args, vals...)
	}
	lots, err := p.queryLots(h, r, where, args...)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	if p.freshenLots(r.Context(), h, lots) {
		if refreshed, err := p.queryLots(h, r, where, args...); err == nil {
			lots = refreshed
		}
	}
	lots = filterLotsByQuery(lots, strings.TrimSpace(r.URL.Query().Get("q")))
	sort.SliceStable(lots, func(i, j int) bool { return lots[i].SavedAt > lots[j].SavedAt })
	writeJSON(w, http.StatusOK, map[string]any{"lots": lots, "latestEventId": latestEventID(r.Context(), h)})
}
