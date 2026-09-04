package bidrl

import (
	"net/http"
)

// The locations you have lots at, as a filter the catalog can offer.

type locationView struct {
	ID       string `json:"id"`
	Name     string `json:"affiliateName"`
	City     string `json:"city"`
	LotCount int    `json:"lotCount"`
}

// handleListLocations serves the locations you actually have lots at, so the
// catalog's location filter offers those and not BidRL's whole SITES menu. It is
// its own request because the catalog must not shrink its own filter options:
// asking the filtered lot list which locations exist would drop every location
// the moment you selected one.
func (p *Plugin) handleListLocations(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	rows, err := h.Store().Query(r.Context(), `SELECT au.affiliate_id, au.affiliate_name, au.city, COUNT(l.id)
		FROM bidrl_auctions au
		LEFT JOIN bidrl_lots l ON l.auction_id = au.id
		WHERE au.affiliate_id != '' AND au.hidden = 0
		GROUP BY au.affiliate_id, au.affiliate_name, au.city
		ORDER BY au.affiliate_name, au.city`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	defer rows.Close()
	out := []locationView{}
	for rows.Next() {
		var loc locationView
		if err := rows.Scan(&loc.ID, &loc.Name, &loc.City, &loc.LotCount); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		out = append(out, loc)
	}
	writeJSON(w, http.StatusOK, map[string]any{"locations": out, "latestEventId": latestEventID(r.Context(), h)})
}
