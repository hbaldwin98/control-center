package bidrl

import (
	"context"
	"fmt"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hostbrowser "github.com/hbaldwin98/control-center/host/browser"
	hostevents "github.com/hbaldwin98/control-center/host/events"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
	hoststorage "github.com/hbaldwin98/control-center/host/storage"
)

type scanArgs struct {
	AuctionID string `json:"auctionId"`
}

type repriceArgs struct {
	LotID string `json:"lotId"`
}

type refreshArgs struct {
	AuctionID string `json:"auctionId"`
}

type enrichArgs struct {
	LotID string `json:"lotId"`
}

func (p *Plugin) scanJob(jc hostjobs.Context) error {
	h, ok := p.host()
	if !ok {
		return fmt.Errorf("bidrl: host is not initialized")
	}
	var args scanArgs
	if err := jc.Args(&args); err != nil {
		return err
	}
	if args.AuctionID == "" {
		return hostjobs.Permanent(fmt.Errorf("bidrl: scan needs an auction id"))
	}
	_ = jc.Logf("scan starting for auction %s", args.AuctionID)
	if err := jc.Progress(0.05, "analyzing lots"); err != nil {
		return err
	}
	if err := p.analyzeUnseen(jc, h, args.AuctionID); err != nil {
		return err
	}
	if err := jc.Progress(0.7, "pricing cited models"); err != nil {
		return err
	}
	if err := p.priceEligible(jc, h, args.AuctionID); err != nil {
		return err
	}
	if err := h.Events().Publish(jc, "scan.completed", args.AuctionID, map[string]any{
		"auctionId": args.AuctionID,
	}); err != nil {
		return err
	}
	return jc.Progress(1, "scan complete")
}

func (p *Plugin) repriceJob(jc hostjobs.Context) error {
	h, ok := p.host()
	if !ok {
		return fmt.Errorf("bidrl: host is not initialized")
	}
	var args repriceArgs
	if err := jc.Args(&args); err != nil {
		return err
	}
	lot, err := p.loadLot(jc, h, args.LotID)
	if err != nil {
		return err
	}
	basis, model, ident, notes, err := p.latestAnalysis(jc, h, lot.ID)
	if err != nil {
		return err
	}
	_ = jc.Logf("repricing lot %s (%s)", lot.ID, model)
	return p.priceLot(jc, h, lot, basis, model, ident, notes, "", true)
}

func (p *Plugin) refreshJob(jc hostjobs.Context) error {
	h, ok := p.host()
	if !ok {
		return fmt.Errorf("bidrl: host is not initialized")
	}
	var args refreshArgs
	if err := jc.Args(&args); err != nil {
		return err
	}
	rows, err := h.Store().Query(jc, `SELECT id, title FROM bidrl_lots WHERE auction_id = ? ORDER BY id`, args.AuctionID)
	if err != nil {
		return err
	}
	type item struct{ ID, Title string }
	var lots []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.ID, &it.Title); err != nil {
			_ = rows.Close()
			return err
		}
		lots = append(lots, it)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	sess, err := h.Browser().Open(jc, hostbrowser.OpenOptions{AllowedHosts: allowedHosts})
	if err != nil {
		return err
	}
	defer sess.Close(jc)
	page, err := sess.NewPage(jc)
	if err != nil {
		return err
	}
	defer page.Close(jc)

	var gallery string
	_ = h.Store().QueryRow(jc, `SELECT url FROM bidrl_auctions WHERE id = ?`, args.AuctionID).Scan(&gallery)
	if gallery != "" {
		_ = page.Goto(jc, gallery)
		_ = page.WaitFor(jc, "body", 15*time.Second)
	}

	pace := newPacer(bidrlMinInterval)
	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	var endsAt string
	for i, lot := range lots {
		if err := jc.Err(); err != nil {
			return err
		}
		_ = jc.Progress(float64(i)/float64(max(1, len(lots))), lot.Title)
		snap, err := p.fetchPusher(jc, page, pace, args.AuctionID, lot.ID)
		if err != nil {
			_ = jc.Logf("pusher %s: %v", lot.ID, err)
			if stopCollect(err) {
				return err
			}
			continue
		}
		ext, reserve := 0, 0
		if snap.BiddingExt {
			ext = 1
		}
		if snap.ReserveMet {
			reserve = 1
		}
		if _, err := h.Store().Exec(jc, `UPDATE bidrl_lots SET current_bid_cents = ?, min_bid_cents = ?, bid_increment_cents = ?,
			bid_count = ?, high_bidder = ?, ends_at = ?, bidding_extended = ?, reserve_met = ?, bids_refreshed_at = ? WHERE id = ?`,
			snap.BidCents, snap.MinBidCents, snap.IncrementCents, snap.BidCount, snap.HighBidder, snap.EndsAt, ext, reserve, now, lot.ID); err != nil {
			return err
		}
		if snap.EndsAt != "" && snap.EndsAt > endsAt {
			endsAt = snap.EndsAt
		}
	}
	if endsAt != "" {
		if _, err := h.Store().Exec(jc, `UPDATE bidrl_auctions SET ends_at = ? WHERE id = ?`, endsAt, args.AuctionID); err != nil {
			return err
		}
	}
	return h.Events().Publish(jc, "bids.refreshed", args.AuctionID, map[string]any{
		"auctionId": args.AuctionID, "at": now, "lots": len(lots),
	})
}

func (p *Plugin) enrichJob(jc hostjobs.Context) error {
	h, ok := p.host()
	if !ok {
		return fmt.Errorf("bidrl: host is not initialized")
	}
	var args enrichArgs
	if err := jc.Args(&args); err != nil {
		return err
	}
	lot, err := p.loadLot(jc, h, args.LotID)
	if err != nil {
		return err
	}
	sess, err := h.Browser().Open(jc, hostbrowser.OpenOptions{AllowedHosts: allowedHosts})
	if err != nil {
		return err
	}
	defer sess.Close(jc)
	page, err := sess.NewPage(jc)
	if err != nil {
		return err
	}
	defer page.Close(jc)
	if lot.URL != "" {
		_ = page.Goto(jc, lot.URL)
		_ = page.WaitFor(jc, "body", 15*time.Second)
	}
	pace := newPacer(bidrlMinInterval)
	rec, err := p.fetchItemData(jc, page, pace, lot.AuctionID, lot.ID)
	if err != nil {
		return err
	}
	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	ext, reserve := 0, 0
	if rec.BiddingExt {
		ext = 1
	}
	if rec.ReserveMet {
		reserve = 1
	}
	lotURL := rec.URL
	if lotURL == "" {
		lotURL = lot.URL
	}
	if _, err := h.Store().Exec(jc, `UPDATE bidrl_lots SET url = ?, lot_code = ?, title = ?, current_bid_cents = ?, min_bid_cents = ?,
		bid_increment_cents = ?, bid_count = ?, high_bidder = ?, ends_at = ?, bidding_extended = ?, reserve_met = ?,
		description = ?, itemdata_at = ? WHERE id = ?`,
		lotURL, rec.LotCode, rec.Title, rec.BidCents, rec.MinBidCents, rec.IncrementCents, rec.BidCount, rec.HighBidder,
		rec.EndsAt, ext, reserve, rec.Description, now, lot.ID); err != nil {
		return err
	}
	if rec.EndsAt != "" {
		if _, err := h.Store().Exec(jc, `UPDATE bidrl_auctions SET ends_at = CASE WHEN ends_at = '' OR ends_at < ? THEN ? ELSE ends_at END WHERE id = ?`,
			rec.EndsAt, rec.EndsAt, lot.AuctionID); err != nil {
			return err
		}
	}
	var n int
	_ = h.Store().QueryRow(jc, `SELECT COUNT(*) FROM bidrl_images WHERE lot_id = ?`, lot.ID).Scan(&n)
	if n == 0 && len(rec.Images) > 0 {
		imgs, err := p.downloadPhotos(jc, h, page, lot.AuctionID, lot.ID, rec.Images)
		if err != nil {
			return err
		}
		for i, img := range imgs {
			if _, err := h.Store().Exec(jc, `INSERT INTO bidrl_images(lot_id, ordinal, blob_key, source_url, mime, size_bytes) VALUES (?, ?, ?, ?, ?, ?)`,
				lot.ID, i+1, img.Key, img.URL, img.MIME, img.Size); err != nil {
				return err
			}
		}
	}
	if err := h.Events().Publish(jc, "lot.enriched", lot.ID, map[string]any{
		"lotId": lot.ID, "auctionId": lot.AuctionID,
	}); err != nil {
		return err
	}
	return jc.Progress(1, "enriched")
}

func (p *Plugin) loadLot(jc hostjobs.Context, h host.Host, id string) (lotRow, error) {
	var lot lotRow
	err := h.Store().QueryRow(jc, `SELECT id, auction_id, url, title, lot_code, current_bid_cents FROM bidrl_lots WHERE id = ?`, id).
		Scan(&lot.ID, &lot.AuctionID, &lot.URL, &lot.Title, &lot.LotCode, &lot.BidCents)
	if err != nil {
		return lot, hostjobs.Permanent(fmt.Errorf("bidrl: unknown lot %s", id))
	}
	return lot, nil
}

func (p *Plugin) latestAnalysis(jc hostjobs.Context, h host.Host, lotID string) (basis, model, ident, notes string, err error) {
	err = h.Store().QueryRow(jc, `SELECT basis, model_or_sku, identification, IFNULL(notes,'') FROM bidrl_analyses WHERE lot_id = ? ORDER BY id DESC LIMIT 1`, lotID).
		Scan(&basis, &model, &ident, &notes)
	if err != nil {
		return "", "", "", "", hostjobs.Permanent(fmt.Errorf("bidrl: lot %s has no analysis; scan first", lotID))
	}
	return basis, model, ident, notes, nil
}

func (p *Plugin) onEvent(ctx context.Context, tx hoststorage.Tx, e hostevents.Event) error {
	_, err := tx.Exec(ctx, `UPDATE bidrl_meta SET last_event_id = ? WHERE id = 1 AND last_event_id < ?`, e.ID, e.ID)
	return err
}

func latestEventID(ctx context.Context, h host.Host) int64 {
	var id int64
	_ = h.Store().QueryRow(ctx, `SELECT last_event_id FROM bidrl_meta WHERE id = 1`).Scan(&id)
	return id
}
