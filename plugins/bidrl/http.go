package bidrl

import (
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
	ID          string `json:"id"`
	URL         string `json:"url"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	LotCount    int    `json:"lotCount"`
	LastError   string `json:"lastError"`
	CollectedAt string `json:"collectedAt"`
	EndsAt      string `json:"endsAt"`
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
	RetrievedAt     string   `json:"retrievedAt"`
	DealScore       *float64 `json:"dealScore"`
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
	rows, err := h.Store().Query(r.Context(), `SELECT id, url, title, status, lot_count, last_error, collected_at, ends_at FROM bidrl_auctions ORDER BY created_at DESC`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	defer rows.Close()
	out := []auctionView{}
	for rows.Next() {
		var a auctionView
		if err := rows.Scan(&a.ID, &a.URL, &a.Title, &a.Status, &a.LotCount, &a.LastError, &a.CollectedAt, &a.EndsAt); err != nil {
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
	if err := h.Store().QueryRow(r.Context(), `SELECT id, url, title, status, lot_count, last_error, collected_at, ends_at FROM bidrl_auctions WHERE id = ?`, id).
		Scan(&a.ID, &a.URL, &a.Title, &a.Status, &a.LotCount, &a.LastError, &a.CollectedAt, &a.EndsAt); err != nil {
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

func (p *Plugin) handleDeleteAuction(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	id := r.PathValue("id")
	if err := p.deleteAuction(r, h, id); err != nil {
		writeHostErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p *Plugin) deleteAuction(r *http.Request, h host.Host, id string) error {
	keys := []string{}
	rows, err := h.Store().Query(r.Context(), `SELECT i.blob_key FROM bidrl_images i JOIN bidrl_lots l ON l.id = i.lot_id WHERE l.auction_id = ?`, id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			_ = rows.Close()
			return err
		}
		keys = append(keys, key)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	err = h.Store().Tx(r.Context(), func(tx hoststorage.Tx) error {
		if _, err := tx.Exec(r.Context(), `DELETE FROM bidrl_images WHERE lot_id IN (SELECT id FROM bidrl_lots WHERE auction_id = ?)`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `DELETE FROM bidrl_analyses WHERE lot_id IN (SELECT id FROM bidrl_lots WHERE auction_id = ?)`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `DELETE FROM bidrl_valuations WHERE lot_id IN (SELECT id FROM bidrl_lots WHERE auction_id = ?)`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `DELETE FROM bidrl_rejections WHERE auction_id = ?`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `DELETE FROM bidrl_lots WHERE auction_id = ?`, id); err != nil {
			return err
		}
		_, err := tx.Exec(r.Context(), `DELETE FROM bidrl_auctions WHERE id = ?`, id)
		return err
	})
	if err != nil {
		return err
	}
	for _, key := range keys {
		_ = h.Blobs().Delete(r.Context(), key)
	}
	return nil
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
	where := "1=1"
	var args []any
	if bucket != "" && bucket != "all" {
		where += ` AND l.bucket = ?`
		args = append(args, bucket)
	}
	if category != "" && category != "all" {
		where += ` AND l.category = ?`
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
	if q != "" {
		filtered := lots[:0]
		for _, lot := range lots {
			score, _ := matchScore(q, lot.Title, lot.Identification, lot.ModelOrSKU, lot.Category, lot.Description)
			if score > 0 {
				filtered = append(filtered, lot)
			}
		}
		lots = filtered
	}
	if ending == "soon" {
		sort.SliceStable(lots, func(i, j int) bool {
			if lots[i].EndsAt == lots[j].EndsAt {
				return lots[i].Title < lots[j].Title
			}
			if lots[i].EndsAt == "" {
				return false
			}
			if lots[j].EndsAt == "" {
				return true
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

func (p *Plugin) handleGetFeed(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	filter := r.URL.Query().Get("filter")
	where := "1=1"
	switch filter {
	case "deals":
		where = `l.bucket = 'priced'`
	case "mislabeled":
		where = `a.title_agreement IS NOT NULL AND a.title_agreement < 0.5 AND l.bucket != 'discarded'`
	case "model":
		where = `a.basis IN ('exact_text','barcode')`
	case "worth_opening":
		where = `l.bucket = 'worth_opening'`
	case "", "all":
		where = `l.bucket != 'pending'`
	default:
		writeErr(w, http.StatusBadRequest, "bad_request", "unknown filter")
		return
	}
	lots, err := p.queryLots(h, r, where)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"filter": filter, "lots": lots, "latestEventId": latestEventID(r.Context(), h)})
}

func (p *Plugin) queryLots(h host.Host, r *http.Request, where string, args ...any) ([]lotView, error) {
	q := `SELECT l.id, l.auction_id, l.url, l.lot_code, l.title, IFNULL(l.description,''), l.current_bid_cents, l.min_bid_cents, l.bid_increment_cents,
		l.bid_count, l.high_bidder, l.ends_at, l.bidding_extended, l.reserve_met, l.category, l.bucket,
		IFNULL(a.identification,''), IFNULL(a.basis,''), IFNULL(a.model_or_sku,''), IFNULL(a.title_agreement, 0),
		v.price_cents, IFNULL(v.kind,''), IFNULL(v.source_url,''), IFNULL(v.cited_text,''), IFNULL(v.source_title,''), IFNULL(v.retrieved_at,''),
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
			&l.PriceCents, &l.PriceKind, &l.SourceURL, &l.CitedText, &l.SourceTitle, &l.RetrievedAt, &thumb); err != nil {
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
		"retrievedAt": lot.RetrievedAt, "dealScore": lot.DealScore,
		"thumbUrl": lot.ThumbURL, "photoUrls": lot.PhotoURLs, "latestEventId": eventID,
	}
}
