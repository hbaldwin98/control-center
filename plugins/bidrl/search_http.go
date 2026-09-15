package bidrl

import (
	"net/http"
	"strings"
	"time"

	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
)

type searchBody struct {
	Query string `json:"query"`
	Scope string `json:"scope"`
}

type searchHitView struct {
	LotID         string  `json:"lotId"`
	AuctionID     string  `json:"auctionId"`
	URL           string  `json:"url"`
	Title         string  `json:"title"`
	AuctionTitle  string  `json:"auctionTitle"`
	LotCode       string  `json:"lotCode"`
	AffiliateID   string  `json:"affiliateId"`
	AffiliateName string  `json:"affiliateName"`
	Preferred     bool    `json:"preferred"`
	BidCents      *int64  `json:"currentBidCents"`
	Score         float64 `json:"matchScore"`
	Reason        string  `json:"matchReason"`
	Source        string  `json:"source"`
	Collected     bool    `json:"collected"`
}

type sitesAuctionView struct {
	ID            string `json:"id"`
	URL           string `json:"url"`
	Title         string `json:"title"`
	AffiliateID   string `json:"affiliateId"`
	AffiliateName string `json:"affiliateName"`
	City          string `json:"city"`
	ItemCount     int    `json:"itemCount"`
	EndsAt        string `json:"endsAt"`
	Collected     bool   `json:"collected"`
}

func (p *Plugin) handlePostSearch(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	var body searchBody
	if !decodeJSON(w, r, &body) {
		return
	}
	q := strings.Join(strings.Fields(body.Query), " ")
	if q == "" || len(q) > 120 {
		writeErr(w, http.StatusBadRequest, "bad_request", "query must be 1–120 characters")
		return
	}
	scope := p.cfg().SearchScope
	switch body.Scope {
	case scopePrefer, scopeOnly, scopeAll:
		scope = body.Scope
	case "":
	default:
		writeErr(w, http.StatusBadRequest, "bad_request", "scope must be prefer, only, or all")
		return
	}
	id := newSearchID(h.Clock().Now())
	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	if err := p.markSearch(r.Context(), h, id, q, scope, "queued", 0, "", now); err != nil {
		writeHostErr(w, err)
		return
	}
	jobID, err := h.Jobs().Enqueue(r.Context(), "search", searchArgs{SearchID: id, Query: q, Scope: scope},
		hostjobs.WithIdempotencyKey("search-"+id))
	if err != nil {
		writeHostErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"jobId": jobID, "searchId": id})
}

func (p *Plugin) handleGetSearch(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	var id, query, scope, status, lastErr, created string
	var hits int
	err := h.Store().QueryRow(r.Context(), `SELECT id, query, scope, status, hit_count, last_error, created_at
		FROM bidrl_searches ORDER BY created_at DESC, id DESC LIMIT 1`).
		Scan(&id, &query, &scope, &status, &hits, &lastErr, &created)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"search": nil, "hits": []searchHitView{}, "latestEventId": latestEventID(r.Context(), h),
		})
		return
	}
	rows, err := h.Store().Query(r.Context(), `SELECT h.lot_id, h.auction_id, h.url, h.title, h.auction_title, h.lot_code,
		h.affiliate_id, h.affiliate_name, h.preferred, h.bid_cents, h.match_score, h.match_reason, h.source,
		l.id IS NOT NULL
		FROM bidrl_search_hits h
		LEFT JOIN bidrl_lots l ON l.id = h.lot_id
		WHERE h.search_id = ? ORDER BY h.ordinal`, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	defer rows.Close()
	out := []searchHitView{}
	for rows.Next() {
		var htv searchHitView
		var pref, collected int
		if err := rows.Scan(&htv.LotID, &htv.AuctionID, &htv.URL, &htv.Title, &htv.AuctionTitle, &htv.LotCode,
			&htv.AffiliateID, &htv.AffiliateName, &pref, &htv.BidCents, &htv.Score, &htv.Reason, &htv.Source, &collected); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		htv.Preferred = pref != 0
		htv.Collected = collected != 0
		out = append(out, htv)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"search": map[string]any{
			"id": id, "query": query, "scope": scope, "status": status,
			"hitCount": hits, "lastError": lastErr, "createdAt": created,
		},
		"hits":          out,
		"latestEventId": latestEventID(r.Context(), h),
	})
}

func (p *Plugin) handleListSites(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	rows, err := h.Store().Query(r.Context(), `SELECT id, url, title, affiliate_id, affiliate_name, city, item_count, ends_at FROM bidrl_affiliate_auctions ORDER BY affiliate_name, title`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	defer rows.Close()
	have := map[string]struct{}{}
	owned, err := h.Store().Query(r.Context(), `SELECT id FROM bidrl_auctions`)
	if err == nil {
		for owned.Next() {
			var id string
			if owned.Scan(&id) == nil {
				have[id] = struct{}{}
			}
		}
		_ = owned.Close()
	}
	out := []sitesAuctionView{}
	for rows.Next() {
		var a sitesAuctionView
		if err := rows.Scan(&a.ID, &a.URL, &a.Title, &a.AffiliateID, &a.AffiliateName, &a.City, &a.ItemCount, &a.EndsAt); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		if a.URL == "" {
			a.URL = auctionURL(a.ID)
		}
		_, a.Collected = have[a.ID]
		out = append(out, a)
	}
	writeJSON(w, http.StatusOK, map[string]any{"auctions": out, "latestEventId": latestEventID(r.Context(), h)})
}

func (p *Plugin) handleRefreshSites(w http.ResponseWriter, r *http.Request) {
	p.enqueueNamed(w, r, "discover", discoverArgs{}, "discover-sites")
}
