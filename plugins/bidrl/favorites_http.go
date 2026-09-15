package bidrl

import (
	"net/http"
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
	page, perPage, err := parseLotPage(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	where := `f.lot_id IS NOT NULL`
	var args []any
	addLotSearch(&where, &args, strings.TrimSpace(r.URL.Query().Get("q")))
	if category := r.URL.Query().Get("category"); category != "" && category != "all" {
		where += ` AND IFNULL(NULLIF(l.category, ''), a.category) = ?`
		args = append(args, category)
	}
	if clause, vals := affiliateClause(r.URL.Query()); clause != "" {
		where += clause
		args = append(args, vals...)
	}
	result, err := p.queryLotsPage(h, r, where, "f.created_at DESC, l.id", page, perPage, args...)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	if p.freshenLots(r.Context(), h, result.Lots) {
		if refreshed, err := p.queryLotsPage(h, r, where, "f.created_at DESC, l.id", result.Page, perPage, args...); err == nil {
			result = refreshed
		}
	}
	writeJSON(w, http.StatusOK, lotPagePayload(result, latestEventID(r.Context(), h)))
}
