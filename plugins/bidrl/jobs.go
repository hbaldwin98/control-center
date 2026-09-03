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
	if err := h.Events().Publish(jc, "scan.completed", args.AuctionID, scanCompleted{AuctionID: args.AuctionID}); err != nil {
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

// refreshJob re-reads one auction's bids from the catalog endpoint. That is a single
// request for every lot in the auction, so this no longer opens a browser session, no
// longer walks the lots, and no longer pays the pacer once per lot.
func (p *Plugin) refreshJob(jc hostjobs.Context) error {
	h, ok := p.host()
	if !ok {
		return fmt.Errorf("bidrl: host is not initialized")
	}
	var args refreshArgs
	if err := jc.Args(&args); err != nil {
		return err
	}
	if args.AuctionID == "" {
		return hostjobs.Permanent(fmt.Errorf("bidrl: refresh needs an auction id"))
	}
	_ = jc.Progress(0.1, "reading the catalog")
	pace := newPacer(bidrlMinInterval)
	bids, err := p.fetchCatalogBids(jc, h, pace, args.AuctionID)
	if err != nil {
		return err
	}
	_ = jc.Progress(0.6, fmt.Sprintf("%d lots", len(bids.Snapshots)))
	changed, err := p.applyCatalogBids(jc, h, args.AuctionID, bids)
	if err != nil {
		return err
	}
	_ = jc.Logf("catalog refresh updated %d of %d lots", changed, len(bids.Snapshots))
	if err := jc.Progress(1, fmt.Sprintf("%d lots refreshed", changed)); err != nil {
		return err
	}
	return h.Events().Publish(jc, "bids.refreshed", args.AuctionID, bidsRefreshed{
		AuctionID: args.AuctionID,
		At:        h.Clock().Now().UTC().Format(time.RFC3339Nano),
		Lots:      changed,
		Source:    "catalog",
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
		bid_increment_cents = ?, bid_count = ?, high_bidder = ?, high_bidder_id = ?, ends_at = ?, bidding_extended = ?, reserve_met = ?,
		description = ?, itemdata_at = ? WHERE id = ?`,
		lotURL, rec.LotCode, rec.Title, rec.BidCents, rec.MinBidCents, rec.IncrementCents, rec.BidCount, rec.HighBidder,
		rec.HighBidderID, rec.EndsAt, ext, reserve, rec.Description, now, lot.ID); err != nil {
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
	if err := h.Events().Publish(jc, "lot.enriched", lot.ID, lotEnriched{LotID: lot.ID, AuctionID: lot.AuctionID}); err != nil {
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
