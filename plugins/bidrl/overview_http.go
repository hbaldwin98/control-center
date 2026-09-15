package bidrl

import (
	"net/http"
	"strings"
	"time"
)

// The two summary screens: the front door, and the deal feed behind the tile.

// lotVisible keeps a lot out of the summary counts when its auction is hidden. It is
// written against the alias `l`, the one every lot query here uses.
const lotVisible = `EXISTS (SELECT 1 FROM bidrl_auctions a WHERE a.id = l.auction_id AND a.hidden = 0)`

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
	// A hidden auction — one that ended holding a saved lot or an undecided finding —
	// is off the auctions list, so it is off these counts too. A front door that says
	// nine auctions over a list of eight is worse than either number alone. Its lots are
	// still on /bidrl/saved and in the findings queue, which is where they are wanted.
	if err := h.Store().QueryRow(r.Context(), `SELECT
		(SELECT COUNT(*) FROM bidrl_auctions WHERE hidden = 0),
		(SELECT COUNT(*) FROM bidrl_lots l WHERE `+lotVisible+`),
		(SELECT COUNT(*) FROM bidrl_lots l WHERE bucket != 'pending' AND `+lotVisible+`),
		(SELECT COUNT(*) FROM bidrl_lots l WHERE `+lotVisible+` AND EXISTS (SELECT 1 FROM bidrl_valuations v WHERE v.lot_id = l.id AND v.price_cents IS NOT NULL))`).
		Scan(&stats.Auctions, &stats.Lots, &stats.Scanned, &stats.Priced); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	stats.Unscanned = stats.Lots - stats.Scanned

	// Close times are stored as text and may be blank or unparsable, so the two
	// time-sensitive counts go through the same parser the rest of the plugin uses rather
	// than trusting SQL string comparison.
	endTimes, err := h.Store().Query(r.Context(), `SELECT l.ends_at FROM bidrl_lots l WHERE l.ends_at != '' AND `+lotVisible)
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

	// These lists used to load every priced lot and every dated lot, then sort and
	// truncate in Go. The response was small, but the backend still decoded the whole
	// catalog on every dashboard request. The ordering is expressible in SQLite, so only
	// the handful of rows the overview can display is materialized.
	deals, err := p.queryLotsLimited(h, r,
		`v.price_cents IS NOT NULL AND v.price_cents > 0
			AND l.current_bid_cents IS NOT NULL AND l.current_bid_cents >= 0
			AND (l.ends_at = '' OR l.ends_at >= ?)
			AND `+lotVisible,
		`CAST(v.price_cents - l.current_bid_cents AS REAL) / v.price_cents DESC, l.id`,
		overviewRows, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	// Keep the parser as the final guard for legacy or malformed rows, which are
	// intentionally treated as unknown rather than as live auctions.
	filteredDeals := deals[:0]
	for _, lot := range deals {
		if lot.DealScore != nil && !hasEnded(lot.EndsAt, now) {
			filteredDeals = append(filteredDeals, lot)
		}
	}
	deals = filteredDeals

	upcoming, err := p.queryLotsLimited(h, r,
		`l.ends_at != '' AND l.ends_at >= ? AND `+lotVisible,
		`l.ends_at, l.id`, overviewRows, now.UTC().Format(time.RFC3339Nano))
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
	// The query is already ordered and bounded; retain the cap for malformed rows.
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
	addLotSearch(&where, &args, q)
	if clause, vals := affiliateClause(r.URL.Query()); clause != "" {
		where += clause
		args = append(args, vals...)
	}
	defaultSort := "lot.asc"
	if filter == "deals" || filter == "worth_opening" {
		defaultSort = "gap.desc"
	}
	order, err := lotOrder(r.URL.Query().Get("sort"), defaultSort)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	result, err := p.queryLotsPage(h, r, where, order, 1, overviewRows, args...)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	if p.freshenLots(r.Context(), h, result.Lots) {
		if refreshed, err := p.queryLotsPage(h, r, where, order, result.Page, overviewRows, args...); err == nil {
			result = refreshed
		}
	}
	payload := lotPagePayload(result, latestEventID(r.Context(), h))
	payload["filter"] = filter
	payload["q"] = q
	writeJSON(w, http.StatusOK, payload)
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
