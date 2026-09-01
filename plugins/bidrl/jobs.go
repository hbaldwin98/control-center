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
	basis, model, ident, err := p.latestAnalysis(jc, h, lot.ID)
	if err != nil {
		return err
	}
	_ = jc.Logf("repricing lot %s (%s)", lot.ID, model)
	return p.priceLot(jc, h, lot, basis, model, ident)
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
	rows, err := h.Store().Query(jc, `SELECT id, url, title FROM bidrl_lots WHERE auction_id = ? ORDER BY id`, args.AuctionID)
	if err != nil {
		return err
	}
	type item struct{ ID, URL, Title string }
	var lots []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.ID, &it.URL, &it.Title); err != nil {
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

	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	for i, lot := range lots {
		if err := jc.Err(); err != nil {
			return err
		}
		_ = jc.Progress(float64(i)/float64(max(1, len(lots))), lot.Title)
		if err := page.Goto(jc, lot.URL); err != nil {
			_ = jc.Logf("refresh %s: %v", lot.URL, err)
			continue
		}
		_ = page.WaitFor(jc, "body", 15*time.Second)
		html, err := page.Content(jc)
		if err != nil {
			return err
		}
		parsed := parseLotHTML(lot.URL, html)
		if parsed.BidCents == nil {
			continue
		}
		if _, err := h.Store().Exec(jc, `UPDATE bidrl_lots SET current_bid_cents = ? WHERE id = ?`, parsed.BidCents, lot.ID); err != nil {
			return err
		}
	}
	return h.Events().Publish(jc, "bids.refreshed", args.AuctionID, map[string]any{
		"auctionId": args.AuctionID, "at": now, "lots": len(lots),
	})
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

func (p *Plugin) latestAnalysis(jc hostjobs.Context, h host.Host, lotID string) (basis, model, ident string, err error) {
	err = h.Store().QueryRow(jc, `SELECT basis, model_or_sku, identification FROM bidrl_analyses WHERE lot_id = ? ORDER BY id DESC LIMIT 1`, lotID).
		Scan(&basis, &model, &ident)
	if err != nil {
		return "", "", "", hostjobs.Permanent(fmt.Errorf("bidrl: lot %s has no analysis; scan first", lotID))
	}
	return basis, model, ident, nil
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
