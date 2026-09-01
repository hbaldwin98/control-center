package bidrl

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/hbaldwin98/control-center/host"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
	hostpolicy "github.com/hbaldwin98/control-center/host/policy"
	hoststorage "github.com/hbaldwin98/control-center/host/storage"
)

type auctionView struct {
	ID            string `json:"id"`
	URL           string `json:"url"`
	Title         string `json:"title"`
	Status        string `json:"status"`
	LotCount      int    `json:"lotCount"`
	LastError     string `json:"lastError"`
	CollectedAt   string `json:"collectedAt"`
	EndsAt        string `json:"endsAt"`
	AffiliateName string `json:"affiliateName"`
	City          string `json:"city"`
}

type lotView struct {
	ID              string   `json:"id"`
	AuctionID       string   `json:"auctionId"`
	URL             string   `json:"url"`
	LotCode         string   `json:"lotCode"`
	Title           string   `json:"title"`
	Description     string   `json:"description"`
	CurrentBidCents *int64   `json:"currentBidCents"`
	MinBidCents     *int64   `json:"minBidCents"`
	IncrementCents  *int64   `json:"bidIncrementCents"`
	BidCount        int      `json:"bidCount"`
	HighBidder      string   `json:"highBidder"`
	EndsAt          string   `json:"endsAt"`
	BiddingExtended bool     `json:"biddingExtended"`
	ReserveMet      bool     `json:"reserveMet"`
	Category        string   `json:"category"`
	Bucket          string   `json:"bucket"`
	Identification  string   `json:"identification"`
	Basis           string   `json:"basis"`
	ModelOrSKU      string   `json:"modelOrSku"`
	TitleAgreement  float64  `json:"titleAgreement"`
	MislabelScore   float64  `json:"mislabelScore"`
	PriceCents      *int64   `json:"priceCents"`
	PriceKind       string   `json:"priceKind"`
	SourceURL       string   `json:"sourceUrl"`
	CitedText       string   `json:"citedText"`
	SourceTitle     string   `json:"sourceTitle"`
	SourceClass     string   `json:"sourceClass"`
	SourceLabel     string   `json:"sourceLabel"`
	ReusedFromLotID string   `json:"reusedFromLotId"`
	RetrievedAt     string   `json:"retrievedAt"`
	DealScore       *float64 `json:"dealScore"`
	MatchScore      *float64 `json:"matchScore,omitempty"`
	MatchReason     string   `json:"matchReason,omitempty"`
	ThumbURL        string   `json:"thumbUrl"`
	PhotoURLs       []string `json:"photoUrls,omitempty"`
}

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
	rows, err := h.Store().Query(r.Context(), `SELECT a.id, a.url, a.title, a.status, a.lot_count, a.last_error, a.collected_at, a.ends_at,
		IFNULL(s.affiliate_name,''), IFNULL(s.city,'')
		FROM bidrl_auctions a
		LEFT JOIN bidrl_affiliate_auctions s ON s.id = a.id
		ORDER BY a.created_at DESC`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	defer rows.Close()
	out := []auctionView{}
	for rows.Next() {
		var a auctionView
		if err := rows.Scan(&a.ID, &a.URL, &a.Title, &a.Status, &a.LotCount, &a.LastError, &a.CollectedAt, &a.EndsAt, &a.AffiliateName, &a.City); err != nil {
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
	if err := h.Store().QueryRow(r.Context(), `SELECT a.id, a.url, a.title, a.status, a.lot_count, a.last_error, a.collected_at, a.ends_at,
		IFNULL(s.affiliate_name,''), IFNULL(s.city,'')
		FROM bidrl_auctions a
		LEFT JOIN bidrl_affiliate_auctions s ON s.id = a.id
		WHERE a.id = ?`, id).
		Scan(&a.ID, &a.URL, &a.Title, &a.Status, &a.LotCount, &a.LastError, &a.CollectedAt, &a.EndsAt, &a.AffiliateName, &a.City); err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "auction not found")
		return
	}
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

type cleanupResult struct {
	Auctions int `json:"auctions"`
	Lots     int `json:"lots"`
	Sites    int `json:"sites"`
}

func (p *Plugin) handleCleanupExpired(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	result, err := p.cleanupExpired(r.Context(), h)
	if err != nil {
		writeHostErr(w, err)
		return
	}
	if err := h.Events().Publish(r.Context(), "expired.cleaned", "expired", map[string]any{
		"auctions": result.Auctions, "lots": result.Lots, "sites": result.Sites,
	}); err != nil {
		writeHostErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (p *Plugin) cleanupExpired(ctx context.Context, h host.Host) (cleanupResult, error) {
	now := h.Clock().Now()
	var result cleanupResult

	auctions, err := p.loadCleanupAuctions(ctx, h)
	if err != nil {
		return result, err
	}
	for _, a := range auctions {
		lotEnds := make([]string, 0, len(a.Lots))
		for _, lot := range a.Lots {
			lotEnds = append(lotEnds, lot.EndsAt)
		}
		if auctionEnded(a.EndsAt, lotEnds, now) {
			if err := p.deleteAuction(ctx, h, a.ID); err != nil {
				return result, err
			}
			result.Auctions++
			continue
		}
		for _, lot := range a.Lots {
			if !hasEnded(lot.EndsAt, now) {
				continue
			}
			if err := p.deleteLot(ctx, h, a.ID, lot.ID); err != nil {
				return result, err
			}
			result.Lots++
		}
	}

	siteRows, err := h.Store().Query(ctx, `SELECT id, ends_at FROM bidrl_affiliate_auctions`)
	if err != nil {
		return result, err
	}
	var siteIDs []string
	for siteRows.Next() {
		var id, ends string
		if err := siteRows.Scan(&id, &ends); err != nil {
			_ = siteRows.Close()
			return result, err
		}
		if hasEnded(ends, now) {
			siteIDs = append(siteIDs, id)
		}
	}
	_ = siteRows.Close()
	if err := siteRows.Err(); err != nil {
		return result, err
	}
	for _, id := range siteIDs {
		if _, err := h.Store().Exec(ctx, `DELETE FROM bidrl_affiliate_auctions WHERE id = ?`, id); err != nil {
			return result, err
		}
		result.Sites++
	}
	return result, nil
}

type cleanupAuction struct {
	ID, EndsAt string
	Lots       []cleanupLot
}

type cleanupLot struct {
	ID, EndsAt string
}

func (p *Plugin) loadCleanupAuctions(ctx context.Context, h host.Host) ([]cleanupAuction, error) {
	rows, err := h.Store().Query(ctx, `SELECT id, ends_at FROM bidrl_auctions`)
	if err != nil {
		return nil, err
	}
	var auctions []cleanupAuction
	for rows.Next() {
		var a cleanupAuction
		if err := rows.Scan(&a.ID, &a.EndsAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		auctions = append(auctions, a)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range auctions {
		lotRows, err := h.Store().Query(ctx, `SELECT id, ends_at FROM bidrl_lots WHERE auction_id = ?`, auctions[i].ID)
		if err != nil {
			return nil, err
		}
		for lotRows.Next() {
			var lot cleanupLot
			if err := lotRows.Scan(&lot.ID, &lot.EndsAt); err != nil {
				_ = lotRows.Close()
				return nil, err
			}
			auctions[i].Lots = append(auctions[i].Lots, lot)
		}
		_ = lotRows.Close()
		if err := lotRows.Err(); err != nil {
			return nil, err
		}
	}
	return auctions, nil
}

func (p *Plugin) deleteAuction(ctx context.Context, h host.Host, id string) error {
	keys, err := p.blobKeys(ctx, h, `SELECT i.blob_key FROM bidrl_images i JOIN bidrl_lots l ON l.id = i.lot_id WHERE l.auction_id = ?`, id)
	if err != nil {
		return err
	}
	err = h.Store().Tx(ctx, func(tx hoststorage.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM bidrl_images WHERE lot_id IN (SELECT id FROM bidrl_lots WHERE auction_id = ?)`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM bidrl_analyses WHERE lot_id IN (SELECT id FROM bidrl_lots WHERE auction_id = ?)`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM bidrl_valuations WHERE lot_id IN (SELECT id FROM bidrl_lots WHERE auction_id = ?)`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM bidrl_rejections WHERE auction_id = ?`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM bidrl_search_hits WHERE auction_id = ?`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM bidrl_intent_hits WHERE lot_id IN (SELECT id FROM bidrl_lots WHERE auction_id = ?)`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM bidrl_lot_embeddings WHERE lot_id IN (SELECT id FROM bidrl_lots WHERE auction_id = ?)`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM bidrl_lots WHERE auction_id = ?`, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM bidrl_auctions WHERE id = ?`, id)
		return err
	})
	if err != nil {
		return err
	}
	for _, key := range keys {
		_ = h.Blobs().Delete(ctx, key)
	}
	return nil
}

func (p *Plugin) deleteLot(ctx context.Context, h host.Host, auctionID, lotID string) error {
	keys, err := p.blobKeys(ctx, h, `SELECT blob_key FROM bidrl_images WHERE lot_id = ?`, lotID)
	if err != nil {
		return err
	}
	err = h.Store().Tx(ctx, func(tx hoststorage.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM bidrl_images WHERE lot_id = ?`, lotID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM bidrl_analyses WHERE lot_id = ?`, lotID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM bidrl_valuations WHERE lot_id = ?`, lotID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM bidrl_rejections WHERE lot_id = ?`, lotID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM bidrl_search_hits WHERE lot_id = ?`, lotID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM bidrl_intent_hits WHERE lot_id = ?`, lotID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM bidrl_lot_embeddings WHERE lot_id = ?`, lotID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM bidrl_lots WHERE id = ?`, lotID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE bidrl_auctions SET lot_count = (SELECT COUNT(*) FROM bidrl_lots WHERE auction_id = ?) WHERE id = ?`, auctionID, auctionID)
		return err
	})
	if err != nil {
		return err
	}
	for _, key := range keys {
		_ = h.Blobs().Delete(ctx, key)
	}
	return nil
}

func (p *Plugin) blobKeys(ctx context.Context, h host.Host, q string, args ...any) ([]string, error) {
	rows, err := h.Store().Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (p *Plugin) handleScan(w http.ResponseWriter, r *http.Request) {
	p.enqueueNamed(w, r, "scan", scanArgs{AuctionID: r.PathValue("id")}, "scan-"+r.PathValue("id"))
}

func (p *Plugin) handleRefresh(w http.ResponseWriter, r *http.Request) {
	p.enqueueNamed(w, r, "refresh", refreshArgs{AuctionID: r.PathValue("id")}, "refresh-"+r.PathValue("id"))
}

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
	lots, err := p.queryLots(h, r, where, args...)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
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

func (p *Plugin) handleGetLot(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
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

func capLots(lots []lotView, n int) []lotView {
	if len(lots) > n {
		return lots[:n]
	}
	return lots
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
	lots, err := p.queryLots(h, r, where)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	lots = filterLotsByQuery(lots, q)
	writeJSON(w, http.StatusOK, map[string]any{"filter": filter, "q": q, "lots": lots, "latestEventId": latestEventID(r.Context(), h)})
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
		(SELECT blob_key FROM bidrl_images WHERE lot_id = l.id ORDER BY ordinal LIMIT 1)
		FROM bidrl_lots l
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
		var ext, reserve int
		if err := rows.Scan(&l.ID, &l.AuctionID, &l.URL, &l.LotCode, &l.Title, &l.Description, &l.CurrentBidCents, &l.MinBidCents, &l.IncrementCents,
			&l.BidCount, &l.HighBidder, &l.EndsAt, &ext, &reserve, &l.Category, &l.Bucket,
			&l.Identification, &l.Basis, &l.ModelOrSKU, &l.TitleAgreement,
			&l.PriceCents, &l.PriceKind, &l.SourceURL, &l.CitedText, &l.SourceTitle, &l.RetrievedAt, &l.ReusedFromLotID, &thumb); err != nil {
			return nil, err
		}
		l.BiddingExtended = ext != 0
		l.ReserveMet = reserve != 0
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

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Body == nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "malformed JSON body")
		return false
	}
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || json.Unmarshal(raw, dst) != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "malformed JSON body")
		return false
	}
	return true
}

func writeHostErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, hostpolicy.ErrPluginDisabled):
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
	case errors.Is(err, hostpolicy.ErrBudgetExceeded):
		writeErr(w, http.StatusConflict, "budget_exceeded", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": msg},
	})
}

func lotPayload(lot lotView, eventID int64) map[string]any {
	return map[string]any{
		"id": lot.ID, "auctionId": lot.AuctionID, "url": lot.URL, "lotCode": lot.LotCode, "title": lot.Title,
		"description": lot.Description, "currentBidCents": lot.CurrentBidCents, "minBidCents": lot.MinBidCents,
		"bidIncrementCents": lot.IncrementCents, "bidCount": lot.BidCount, "highBidder": lot.HighBidder,
		"endsAt": lot.EndsAt, "biddingExtended": lot.BiddingExtended, "reserveMet": lot.ReserveMet,
		"category": lot.Category, "bucket": lot.Bucket, "identification": lot.Identification, "basis": lot.Basis,
		"modelOrSku": lot.ModelOrSKU, "titleAgreement": lot.TitleAgreement, "mislabelScore": lot.MislabelScore,
		"priceCents": lot.PriceCents, "priceKind": lot.PriceKind, "sourceUrl": lot.SourceURL, "citedText": lot.CitedText,
		"sourceTitle": lot.SourceTitle, "sourceClass": lot.SourceClass, "sourceLabel": lot.SourceLabel,
		"reusedFromLotId": lot.ReusedFromLotID, "retrievedAt": lot.RetrievedAt, "dealScore": lot.DealScore,
		"thumbUrl": lot.ThumbURL, "photoUrls": lot.PhotoURLs, "latestEventId": eventID,
	}
}
