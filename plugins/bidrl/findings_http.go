package bidrl

import (
	"net/http"
	"strings"
	"time"

	hoststorage "github.com/hbaldwin98/control-center/host/storage"
)

// The review queue: what a run turned up, and accepting or rejecting one of them.

type findingView struct {
	ID          string  `json:"id"`
	WatchlistID string  `json:"watchlistId"`
	Watchlist   string  `json:"watchlist"`
	Score       float64 `json:"score"`
	Reason      string  `json:"reason"`
	State       string  `json:"state"`
	CreatedAt   string  `json:"createdAt"`
	Lot         lotView `json:"lot"`
}

func (p *Plugin) handleListFindings(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	state := r.URL.Query().Get("state")
	if state == "" {
		state = "new"
	}
	switch state {
	case "new", "accepted", "rejected", "all":
	default:
		writeErr(w, http.StatusBadRequest, "bad_request", "unknown state")
		return
	}
	where := `1=1`
	var args []any
	if state != "all" {
		where += ` AND f.state = ?`
		args = append(args, state)
	}
	if wl := strings.TrimSpace(r.URL.Query().Get("watchlist")); wl != "" {
		where += ` AND f.watchlist_id = ?`
		args = append(args, wl)
	}
	rows, err := h.Store().Query(r.Context(), `SELECT f.id, f.watchlist_id, IFNULL(w.name,''), f.lot_id,
		f.score, f.reason, f.state, f.created_at
		FROM bidrl_findings f
		LEFT JOIN bidrl_watchlists w ON w.id = f.watchlist_id
		WHERE `+where+`
		ORDER BY f.created_at DESC, f.score DESC, f.id`, args...)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	defer rows.Close()
	type row struct {
		findingView
		LotID string
	}
	var listed []row
	for rows.Next() {
		var rv row
		if err := rows.Scan(&rv.ID, &rv.WatchlistID, &rv.Watchlist, &rv.LotID,
			&rv.Score, &rv.Reason, &rv.State, &rv.CreatedAt); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		listed = append(listed, rv)
	}
	if err := rows.Err(); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	lotIDs := make([]string, 0, len(listed))
	for _, rv := range listed {
		lotIDs = append(lotIDs, rv.LotID)
	}
	lotWhere, lotArgs := lotIDWhere(lotIDs)
	lots, err := p.queryLots(h, r, lotWhere, lotArgs...)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	byID := make(map[string]lotView, len(lots))
	for _, lot := range lots {
		byID[lot.ID] = lot
	}
	out := []findingView{}
	for _, rv := range listed {
		lot, ok := byID[rv.LotID]
		if !ok {
			continue
		}
		view := rv.findingView
		view.Lot = lot
		out = append(out, view)
	}
	watchlists, err := p.listWatchlists(r.Context(), h)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"findings": out, "watchlists": watchlists, "state": state,
		"latestEventId": latestEventID(r.Context(), h),
	})
}

func (p *Plugin) handleAcceptFinding(w http.ResponseWriter, r *http.Request) {
	p.decideFinding(w, r, "accepted")
}

func (p *Plugin) handleRejectFinding(w http.ResponseWriter, r *http.Request) {
	p.decideFinding(w, r, "rejected")
}

// decideFinding records the verdict. Accepting also saves the lot: accepting
// something and then hunting for it in another tab is the obvious wrong flow.
func (p *Plugin) decideFinding(w http.ResponseWriter, r *http.Request, state string) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	id := r.PathValue("id")
	var lotID string
	if err := h.Store().QueryRow(r.Context(), `SELECT lot_id FROM bidrl_findings WHERE id = ?`, id).Scan(&lotID); err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "finding not found")
		return
	}
	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	err := h.Store().Tx(r.Context(), func(tx hoststorage.Tx) error {
		if _, err := tx.Exec(r.Context(), `UPDATE bidrl_findings SET state = ?, decided_at = ? WHERE id = ?`,
			state, now, id); err != nil {
			return err
		}
		if state != "accepted" {
			return nil
		}
		_, err := tx.Exec(r.Context(), `INSERT INTO bidrl_favorites(lot_id, note, created_at)
			VALUES (?, '', ?) ON CONFLICT(lot_id) DO NOTHING`, lotID, now)
		return err
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	_ = h.Events().Publish(r.Context(), "finding.decided", id, findingDecided{FindingID: id, LotID: lotID, State: state})
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "state": state, "lotId": lotID})
}

// ------------------------------------------------------------------ cleaning
