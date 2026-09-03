package bidrl

import (
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/hbaldwin98/control-center/host"
)

// Lots over HTTP: the catalog, one lot, and the jobs that reprice or enrich it.

func (p *Plugin) handleReprice(w http.ResponseWriter, r *http.Request) {
	p.enqueueNamed(w, r, "reprice", repriceArgs{LotID: r.PathValue("id")}, "reprice-"+r.PathValue("id"))
}

func (p *Plugin) handleEnrich(w http.ResponseWriter, r *http.Request) {
	p.enqueueNamed(w, r, "enrich", enrichArgs{LotID: r.PathValue("id")}, "enrich-"+r.PathValue("id"))
}

func (p *Plugin) handleListLots(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	bucket := r.URL.Query().Get("bucket")
	category := r.URL.Query().Get("category")
	ending := r.URL.Query().Get("ending")
	where, ok := feedWhere(r.URL.Query().Get("filter"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_request", "unknown filter")
		return
	}
	var args []any
	if bucket != "" && bucket != "all" {
		where += ` AND l.bucket = ?`
		args = append(args, bucket)
	}
	if category != "" && category != "all" {
		where += ` AND IFNULL(NULLIF(l.category, ''), a.category) = ?`
		args = append(args, category)
	}
	if ending == "soon" {
		where += ` AND l.ends_at != ''`
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
	lots = filterLotsByQuery(lots, q)
	if ending == "soon" {
		now := h.Clock().Now()
		filtered := lots[:0]
		for _, lot := range lots {
			if endingSoon(lot.EndsAt, now) {
				filtered = append(filtered, lot)
			}
		}
		lots = filtered
		sort.SliceStable(lots, func(i, j int) bool {
			if lots[i].EndsAt == lots[j].EndsAt {
				return lots[i].Title < lots[j].Title
			}
			return lots[i].EndsAt < lots[j].EndsAt
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"lots": lots, "latestEventId": latestEventID(r.Context(), h)})
}

func (p *Plugin) handleGetLot(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	// One lot is one small request, so the screen people actually watch a price on is
	// current when it opens.
	p.freshenLot(r.Context(), h, r.PathValue("id"))
	lots, err := p.queryLots(h, r, `l.id = ?`, r.PathValue("id"))
	if err != nil || len(lots) == 0 {
		writeErr(w, http.StatusNotFound, "not_found", "lot not found")
		return
	}
	lot := lots[0]
	rows, err := h.Store().Query(r.Context(), `SELECT blob_key FROM bidrl_images WHERE lot_id = ? ORDER BY ordinal`, lot.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	defer rows.Close()
	lot.PhotoURLs = []string{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		lot.PhotoURLs = append(lot.PhotoURLs, h.Blobs().URL(key))
	}
	writeJSON(w, http.StatusOK, lotPayload(lot, latestEventID(r.Context(), h)))
}

func filterLotsByQuery(lots []lotView, q string) []lotView {
	q = strings.TrimSpace(q)
	if q == "" {
		return lots
	}
	filtered := lots[:0]
	for _, lot := range lots {
		score, _ := matchScore(q, lot.Title, lot.Identification, lot.ModelOrSKU, lot.Category, lot.Description)
		if score > 0 {
			filtered = append(filtered, lot)
		}
	}
	return filtered
}

func (p *Plugin) queryLots(h host.Host, r *http.Request, where string, args ...any) ([]lotView, error) {
	q := `SELECT l.id, l.auction_id, l.url, l.lot_code, l.title, IFNULL(l.description,''), l.current_bid_cents, l.min_bid_cents, l.bid_increment_cents,
		l.bid_count, l.high_bidder, l.ends_at, l.bidding_extended, l.reserve_met, l.category, l.bucket,
		IFNULL(a.identification,''), IFNULL(a.basis,''), IFNULL(a.model_or_sku,''), IFNULL(a.title_agreement, 0),
		v.price_cents, IFNULL(v.kind,''), IFNULL(v.source_url,''), IFNULL(v.cited_text,''), IFNULL(v.source_title,''), IFNULL(v.retrieved_at,''), IFNULL(v.reused_from_lot_id,''),
		IFNULL(au.affiliate_id,''), IFNULL(au.affiliate_name,''), IFNULL(au.city,''),
		IFNULL(f.note,''), IFNULL(f.created_at,''), f.lot_id IS NOT NULL,
		(SELECT blob_key FROM bidrl_images WHERE lot_id = l.id ORDER BY ordinal LIMIT 1)
		FROM bidrl_lots l
		LEFT JOIN bidrl_auctions au ON au.id = l.auction_id
		LEFT JOIN bidrl_favorites f ON f.lot_id = l.id
		LEFT JOIN bidrl_analyses a ON a.id = (SELECT MAX(id) FROM bidrl_analyses WHERE lot_id = l.id)
		LEFT JOIN bidrl_valuations v ON v.id = (SELECT MAX(id) FROM bidrl_valuations WHERE lot_id = l.id)
		WHERE ` + where + `
		ORDER BY l.id`
	rows, err := h.Store().Query(r.Context(), q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []lotView{}
	for rows.Next() {
		var l lotView
		var thumb *string
		var ext, reserve, fav int
		if err := rows.Scan(&l.ID, &l.AuctionID, &l.URL, &l.LotCode, &l.Title, &l.Description, &l.CurrentBidCents, &l.MinBidCents, &l.IncrementCents,
			&l.BidCount, &l.HighBidder, &l.EndsAt, &ext, &reserve, &l.Category, &l.Bucket,
			&l.Identification, &l.Basis, &l.ModelOrSKU, &l.TitleAgreement,
			&l.PriceCents, &l.PriceKind, &l.SourceURL, &l.CitedText, &l.SourceTitle, &l.RetrievedAt, &l.ReusedFromLotID,
			&l.AffiliateID, &l.AffiliateName, &l.City,
			&l.FavoriteNote, &l.SavedAt, &fav, &thumb); err != nil {
			return nil, err
		}
		l.BiddingExtended = ext != 0
		l.ReserveMet = reserve != 0
		l.Favorite = fav != 0
		l.MislabelScore = mislabelScore(l.TitleAgreement)
		if l.PriceCents != nil {
			if d, ok := dealScore(l.CurrentBidCents, *l.PriceCents); ok {
				l.DealScore = &d
			}
		}
		if l.SourceURL != "" {
			src := classifySource(l.SourceURL)
			l.SourceClass = src.Class
			l.SourceLabel = src.Label
		}
		if thumb != nil && *thumb != "" {
			l.ThumbURL = h.Blobs().URL(*thumb)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// affiliateClause narrows a lot query to a set of SITES locations. Filtering to
// several at once is the point: the useful question is "what is within a drive",
// which is a handful of locations, not one and not all of them.
//
// Accepts repeated ?affiliate= and comma-joined values alike. No parameter means
// every location, so links written before this filter existed still work.
func affiliateClause(q url.Values) (string, []any) {
	seen := map[string]struct{}{}
	var ids []any
	for _, raw := range q["affiliate"] {
		for _, part := range strings.Split(raw, ",") {
			id := affiliateIDFromSlug(part)
			if id == "" {
				continue
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return "", nil
	}
	return " AND au.affiliate_id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")", ids
}
