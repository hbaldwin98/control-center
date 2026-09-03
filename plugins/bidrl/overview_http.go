package bidrl

import (
	"net/http"
	"sort"
	"strings"
)

// The two summary screens: the front door, and the deal feed behind the tile.

// handleGetOverview answers the front door in one request.
//
// The overview wants counts, the widest gaps, and what closes next. Computing that in the
// browser means shipping every lot row — descriptions, valuations, photo URLs — to count
// them, which is the wrong thing to put on a phone. Counting happens in SQL; only the two
// short lists come back as rows.
func (p *Plugin) handleGetOverview(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	now := h.Clock().Now()
	var stats struct {
		Auctions  int `json:"auctions"`
		Lots      int `json:"lots"`
		Scanned   int `json:"scanned"`
		Unscanned int `json:"unscanned"`
		Priced    int `json:"priced"`
		Live      int `json:"live"`
		Ending    int `json:"ending"`
	}
	if err := h.Store().QueryRow(r.Context(), `SELECT
		(SELECT COUNT(*) FROM bidrl_auctions),
		(SELECT COUNT(*) FROM bidrl_lots),
		(SELECT COUNT(*) FROM bidrl_lots WHERE bucket != 'pending'),
		(SELECT COUNT(*) FROM bidrl_lots l WHERE EXISTS (SELECT 1 FROM bidrl_valuations v WHERE v.lot_id = l.id AND v.price_cents IS NOT NULL))`).
		Scan(&stats.Auctions, &stats.Lots, &stats.Scanned, &stats.Priced); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	stats.Unscanned = stats.Lots - stats.Scanned

	// Close times are stored as text and may be blank or unparsable, so the two
	// time-sensitive counts go through the same parser the rest of the plugin uses rather
	// than trusting SQL string comparison.
	endTimes, err := h.Store().Query(r.Context(), `SELECT ends_at FROM bidrl_lots WHERE ends_at != ''`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	blank := stats.Lots
	for endTimes.Next() {
		var endsAt string
		if err := endTimes.Scan(&endsAt); err != nil {
			endTimes.Close()
			writeErr(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		blank--
		if hasEnded(endsAt, now) {
			continue
		}
		stats.Live++
		if endingSoon(endsAt, now) {
			stats.Ending++
		}
	}
	endTimes.Close()
	if err := endTimes.Err(); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	// A lot with no close time has not ended, so it is still live.
	stats.Live += blank

	priced, err := p.queryLots(h, r, `v.price_cents IS NOT NULL`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	deals := make([]lotView, 0, len(priced))
	for _, lot := range priced {
		if lot.DealScore != nil && !hasEnded(lot.EndsAt, now) {
			deals = append(deals, lot)
		}
	}
	sort.SliceStable(deals, func(i, j int) bool { return *deals[i].DealScore > *deals[j].DealScore })
	deals = capLots(deals, overviewRows)

	upcoming, err := p.queryLots(h, r, `l.ends_at != ''`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	closing := make([]lotView, 0, len(upcoming))
	for _, lot := range upcoming {
		if !hasEnded(lot.EndsAt, now) {
			closing = append(closing, lot)
		}
	}
	sort.SliceStable(closing, func(i, j int) bool { return closing[i].EndsAt < closing[j].EndsAt })
	closing = capLots(closing, overviewRows)

	writeJSON(w, http.StatusOK, map[string]any{
		"stats":         stats,
		"deals":         deals,
		"closing":       closing,
		"latestEventId": latestEventID(r.Context(), h),
	})
}

func (p *Plugin) handleGetFeed(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	filter := r.URL.Query().Get("filter")
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	where, ok := feedWhere(filter)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_request", "unknown filter")
		return
	}
	var args []any
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
	lots = filterLotsByQuery(lots, q)
	writeJSON(w, http.StatusOK, map[string]any{"filter": filter, "q": q, "lots": lots, "latestEventId": latestEventID(r.Context(), h)})
}

// feedWhere turns a named feed preset into its SQL predicate. The catalog accepts the
// same names, so a preset and the bucket/category filters are one screen rather than two
// listings of the same table.
func feedWhere(filter string) (string, bool) {
	switch filter {
	case "deals":
		return `l.bucket = 'priced'`, true
	case "mislabeled":
		return `a.title_agreement IS NOT NULL AND a.title_agreement < 0.5 AND l.bucket != 'discarded'`, true
	case "model":
		return `a.basis IN ('exact_text','barcode')`, true
	case "worth_opening":
		return `l.bucket = 'worth_opening'`, true
	case "scanned":
		return `l.bucket != 'pending'`, true
	case "", "all":
		return `1=1`, true
	default:
		return "", false
	}
}
