package bidrl

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hoststorage "github.com/hbaldwin98/control-center/host/storage"
)

// Lots over HTTP: the catalog, one lot, and the jobs that reprice or enrich it.

const (
	defaultLotsPerPage = 50
	maxLotsPerPage     = 100
	maxLotPage         = 1_000_000
)

const lotGapOrderExpr = `CASE WHEN v.price_cents > 0 AND l.current_bid_cents >= 0
	THEN (v.price_cents - l.current_bid_cents) * 1.0 / v.price_cents END`

type lotListPage struct {
	Lots       []lotView
	Total      int
	Page       int
	PerPage    int
	TotalPages int
}

const lotSelect = `SELECT l.id, l.auction_id, l.url, l.lot_code, l.title, IFNULL(l.description,''), l.current_bid_cents, l.min_bid_cents, l.bid_increment_cents,
		l.bid_count, l.high_bidder, l.ends_at, l.bidding_extended, l.reserve_met, l.category, l.bucket,
		IFNULL(a.identification,''), IFNULL(a.basis,''), IFNULL(a.model_or_sku,''), IFNULL(a.title_agreement, 0),
		v.price_cents, IFNULL(v.kind,''), IFNULL(v.source_url,''), IFNULL(v.cited_text,''), IFNULL(v.source_title,''), IFNULL(v.retrieved_at,''), IFNULL(v.reused_from_lot_id,''),
		IFNULL(au.affiliate_id,''), IFNULL(au.affiliate_name,''), IFNULL(au.city,''),
		IFNULL(f.note,''), IFNULL(f.created_at,''), f.lot_id IS NOT NULL,
		(SELECT blob_key FROM bidrl_images WHERE lot_id = l.id ORDER BY ordinal LIMIT 1)`

const lotBaseFrom = ` FROM bidrl_lots l
		LEFT JOIN bidrl_auctions au ON au.id = l.auction_id
		LEFT JOIN bidrl_favorites f ON f.lot_id = l.id`

const lotAnalysisFrom = `
		LEFT JOIN bidrl_analyses a ON a.id = (SELECT MAX(id) FROM bidrl_analyses WHERE lot_id = l.id)`

const lotValuationFrom = `
		LEFT JOIN bidrl_valuations v ON v.id = (SELECT MAX(id) FROM bidrl_valuations WHERE lot_id = l.id)`

const lotFrom = lotBaseFrom + lotAnalysisFrom + lotValuationFrom

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
	page, perPage, err := parseLotPage(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	bucket := r.URL.Query().Get("bucket")
	category := r.URL.Query().Get("category")
	ending := r.URL.Query().Get("ending")
	filter := r.URL.Query().Get("filter")
	where, ok := feedWhere(filter)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad_request", "unknown filter")
		return
	}
	var args []any
	addLotSearch(&where, &args, q)
	if bucket != "" && bucket != "all" {
		where += ` AND l.bucket = ?`
		args = append(args, bucket)
	}
	if category != "" && category != "all" {
		where += ` AND IFNULL(NULLIF(l.category, ''), a.category) = ?`
		args = append(args, category)
	}
	defaultSort := "lot.asc"
	if ending == "soon" {
		now := h.Clock().Now().UTC()
		where += ` AND l.ends_at > ? AND l.ends_at <= ?`
		args = append(args, now.Format(time.RFC3339Nano), now.Add(24*time.Hour).Format(time.RFC3339Nano))
		defaultSort = "ends.asc"
	} else if filter == "deals" || bucket == "priced" || bucket == "worth_opening" {
		defaultSort = "gap.desc"
	}
	order, err := lotOrder(r.URL.Query().Get("sort"), defaultSort)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if clause, vals := affiliateClause(r.URL.Query()); clause != "" {
		where += clause
		args = append(args, vals...)
	}
	result, err := p.queryLotsPage(h, r, where, order, page, perPage, args...)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	if p.freshenLots(r.Context(), h, result.Lots) {
		if refreshed, err := p.queryLotsPage(h, r, where, order, result.Page, perPage, args...); err == nil {
			result = refreshed
		}
	}
	writeJSON(w, http.StatusOK, lotPagePayload(result, latestEventID(r.Context(), h)))
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

func (p *Plugin) queryLots(h host.Host, r *http.Request, where string, args ...any) ([]lotView, error) {
	rows, err := h.Store().Query(r.Context(), lotSelect+lotFrom+` WHERE `+where+` ORDER BY l.id`, args...)
	if err != nil {
		return nil, err
	}
	return scanLotRows(rows, h.Blobs().URL)
}

func (p *Plugin) queryLotsPage(h host.Host, r *http.Request, where, order string, page, perPage int, args ...any) (lotListPage, error) {
	var total int
	if err := h.Store().QueryRow(r.Context(), `SELECT COUNT(DISTINCT l.id)`+lotCountFrom(where)+` WHERE `+where, args...).Scan(&total); err != nil {
		return lotListPage{}, err
	}
	totalPages := 0
	if total > 0 {
		totalPages = (total + perPage - 1) / perPage
		if page > totalPages {
			page = totalPages
		}
	}
	if page < 1 {
		page = 1
	}
	queryArgs := append(append([]any(nil), args...), perPage, (page-1)*perPage)
	rows, err := h.Store().Query(r.Context(), lotSelect+lotFrom+` WHERE `+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return lotListPage{}, err
	}
	lots, err := scanLotRows(rows, h.Blobs().URL)
	if err != nil {
		return lotListPage{}, err
	}
	return lotListPage{Lots: lots, Total: total, Page: page, PerPage: perPage, TotalPages: totalPages}, nil
}

// lotCountFrom deliberately avoids the latest analysis and valuation joins unless the
// predicate needs them. The list query must still join both tables to render a row, but
// counting a page should not walk the historical analysis/valuation tables for every lot.
func lotCountFrom(where string) string {
	from := lotBaseFrom
	if strings.Contains(where, "a.") {
		from += lotAnalysisFrom
	}
	if strings.Contains(where, "v.") {
		from += lotValuationFrom
	}
	return from
}

func (p *Plugin) queryLotsLimited(h host.Host, r *http.Request, where, order string, limit int, args ...any) ([]lotView, error) {
	queryArgs := append(append([]any(nil), args...), limit)
	rows, err := h.Store().Query(r.Context(), lotSelect+lotFrom+` WHERE `+where+` ORDER BY `+order+` LIMIT ?`, queryArgs...)
	if err != nil {
		return nil, err
	}
	return scanLotRows(rows, h.Blobs().URL)
}

func scanLotRows(rows hoststorage.Rows, blobURL func(string) string) ([]lotView, error) {
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
			l.ThumbURL = blobURL(*thumb)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func lotOrder(raw, fallback string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		raw = fallback
	}
	parts := strings.Split(raw, ".")
	if len(parts) > 2 || strings.TrimSpace(parts[0]) == "" {
		return "", &queryValueError{message: "sort must be a known column and direction"}
	}
	key := strings.TrimSpace(parts[0])
	direction := ""
	if len(parts) == 2 {
		direction = strings.ToLower(strings.TrimSpace(parts[1]))
	}
	if direction == "" {
		switch key {
		case "gap", "price", "saved":
			direction = "desc"
		default:
			direction = "asc"
		}
	}
	if direction != "asc" && direction != "desc" {
		return "", &queryValueError{message: "sort direction must be asc or desc"}
	}
	expressions := map[string]string{
		"lot":      "l.id",
		"name":     "l.title",
		"bid":      "l.current_bid_cents",
		"ends":     "l.ends_at",
		"location": "COALESCE(NULLIF(au.city, ''), au.affiliate_name)",
		"category": "COALESCE(NULLIF(l.category, ''), a.category)",
		"price":    "v.price_cents",
		"gap":      lotGapOrderExpr,
		"bucket":   "l.bucket",
		"saved":    "f.created_at",
	}
	expr, ok := expressions[key]
	if !ok {
		return "", &queryValueError{message: "sort must be a known column and direction"}
	}
	numeric := key == "bid" || key == "price" || key == "gap"
	if numeric {
		return `CASE WHEN (` + expr + `) IS NULL THEN 1 ELSE 0 END, (` + expr + `) ` + direction + `, l.id`, nil
	}
	return `CASE WHEN IFNULL(` + expr + `, '') = '' THEN 1 ELSE 0 END, ` + expr + ` ` + direction + `, l.id`, nil
}

func parseLotPage(r *http.Request) (int, int, error) {
	page, err := positiveQueryInt(r, "page", 1)
	if err != nil {
		return 0, 0, err
	}
	perPage, err := positiveQueryInt(r, "perPage", defaultLotsPerPage)
	if err != nil {
		return 0, 0, err
	}
	if perPage > maxLotsPerPage {
		return 0, 0, &queryValueError{message: "perPage must be at most 100"}
	}
	return page, perPage, nil
}

func positiveQueryInt(r *http.Request, name string, fallback int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, &queryValueError{message: name + " must be a positive integer"}
	}
	if n > maxLotPage {
		return 0, &queryValueError{message: name + " is too large"}
	}
	return n, nil
}

type queryValueError struct{ message string }

func (e *queryValueError) Error() string { return e.message }

func lotPagePayload(page lotListPage, eventID int64) map[string]any {
	return map[string]any{
		"lots":          page.Lots,
		"page":          page.Page,
		"perPage":       page.PerPage,
		"total":         page.Total,
		"totalPages":    page.TotalPages,
		"hasNext":       page.Page < page.TotalPages,
		"latestEventId": eventID,
	}
}

func addLotSearch(where *string, args *[]any, q string) {
	if strings.TrimSpace(q) == "" {
		return
	}
	tokens := contentTokens(q)
	if len(tokens) == 0 {
		*where += ` AND 0`
		return
	}
	terms := make([]string, 0, len(tokens))
	for _, token := range tokens {
		terms = append(terms, `(LOWER(IFNULL(l.title,'')) LIKE ? OR LOWER(IFNULL(a.identification,'')) LIKE ? OR LOWER(IFNULL(a.model_or_sku,'')) LIKE ? OR LOWER(IFNULL(l.category,'')) LIKE ? OR LOWER(IFNULL(l.description,'')) LIKE ?)`)
		pattern := "%" + strings.ToLower(token) + "%"
		for i := 0; i < 5; i++ {
			*args = append(*args, pattern)
		}
	}
	*where += ` AND (` + strings.Join(terms, " OR ") + `)`
}

// lotIDWhere lets endpoints that already know the lot ids they need avoid loading the
// entire catalog just to assemble a response (findings and intent results use this).
func lotIDWhere(ids []string) (string, []any) {
	args := make([]any, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		args = append(args, id)
	}
	if len(args) == 0 {
		return "0", nil
	}
	return "l.id IN (" + placeholders(len(args)) + ")", args
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
