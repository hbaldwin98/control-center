package bidrl

import (
	"net/http"

	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
)

// Auctions over HTTP: adding one, listing them, reading one, and deleting it.

func (p *Plugin) handleAddAuction(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	var body collectArgs
	if !decodeJSON(w, r, &body) {
		return
	}
	_, _, auctionID, err := parseAuctionURL(body.URL)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	id, err := h.Jobs().Enqueue(r.Context(), "collect", body, hostjobs.WithIdempotencyKey("auction-"+auctionID))
	if err != nil {
		writeHostErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"jobId": id, "auctionId": auctionID})
}

func (p *Plugin) handleListAuctions(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	rows, err := h.Store().Query(r.Context(), `SELECT id, url, title, status, lot_count, last_error, collected_at, ends_at,
		affiliate_id, affiliate_name, city
		FROM bidrl_auctions
		WHERE hidden = 0
		ORDER BY created_at DESC`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	defer rows.Close()
	out := []auctionView{}
	for rows.Next() {
		var a auctionView
		if err := rows.Scan(&a.ID, &a.URL, &a.Title, &a.Status, &a.LotCount, &a.LastError, &a.CollectedAt, &a.EndsAt, &a.AffiliateID, &a.AffiliateName, &a.City); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		out = append(out, a)
	}
	writeJSON(w, http.StatusOK, map[string]any{"auctions": out, "latestEventId": latestEventID(r.Context(), h)})
}

func (p *Plugin) handleGetAuction(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	id := r.PathValue("id")
	var a auctionView
	if err := h.Store().QueryRow(r.Context(), `SELECT id, url, title, status, lot_count, last_error, collected_at, ends_at,
		affiliate_id, affiliate_name, city
		FROM bidrl_auctions
		WHERE id = ?`, id).
		Scan(&a.ID, &a.URL, &a.Title, &a.Status, &a.LotCount, &a.LastError, &a.CollectedAt, &a.EndsAt, &a.AffiliateID, &a.AffiliateName, &a.City); err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "auction not found")
		return
	}
	// One catalog request covers every lot below, so the page can be current on open
	// instead of waiting for someone to press refresh.
	p.freshenAuctions(r.Context(), h, []string{id})
	lots, err := p.queryLots(h, r, `l.auction_id = ?`, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"auction": a, "lots": lots, "latestEventId": latestEventID(r.Context(), h)})
}

// handleGetAuctionIndex serves just enough of an auction's lots to page through them:
// the lot screen wants a previous, a next, and a position, and downloading every full lot
// row — descriptions, valuations, photo URLs — to compute three of them is a payload no
// phone should pay for.
func (p *Plugin) handleGetAuctionIndex(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	id := r.PathValue("id")
	var title string
	if err := h.Store().QueryRow(r.Context(), `SELECT title FROM bidrl_auctions WHERE id = ?`, id).Scan(&title); err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "auction not found")
		return
	}
	// Ordered the way the auction screen lists them, so "next" means the next row there.
	rows, err := h.Store().Query(r.Context(), `SELECT id, lot_code, title FROM bidrl_lots WHERE auction_id = ? ORDER BY id`, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	defer rows.Close()
	type indexEntry struct {
		ID      string `json:"id"`
		LotCode string `json:"lotCode"`
		Title   string `json:"title"`
	}
	lots := []indexEntry{}
	for rows.Next() {
		var e indexEntry
		if err := rows.Scan(&e.ID, &e.LotCode, &e.Title); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		lots = append(lots, e)
	}
	if err := rows.Err(); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"title": title, "lots": lots, "latestEventId": latestEventID(r.Context(), h)})
}

func (p *Plugin) handleDeleteAuction(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	id := r.PathValue("id")
	if err := p.deleteAuction(r.Context(), h, id); err != nil {
		writeHostErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p *Plugin) handleScan(w http.ResponseWriter, r *http.Request) {
	p.enqueueNamed(w, r, "scan", scanArgs{AuctionID: r.PathValue("id")}, "scan-"+r.PathValue("id"))
}

func (p *Plugin) handleRefresh(w http.ResponseWriter, r *http.Request) {
	p.enqueueNamed(w, r, "refresh", refreshArgs{AuctionID: r.PathValue("id")}, "refresh-"+r.PathValue("id"))
}

func (p *Plugin) enqueueNamed(w http.ResponseWriter, r *http.Request, name string, args any, key string) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	id, err := h.Jobs().Enqueue(r.Context(), name, args, hostjobs.WithIdempotencyKey(key))
	if err != nil {
		writeHostErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int64{"jobId": id})
}
