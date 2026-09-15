package bidrl

import (
	"context"
	"net/http"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hoststorage "github.com/hbaldwin98/control-center/host/storage"
)

// Removing auctions that have ended, and the rows and blobs behind them.

type cleanupResult struct {
	Auctions int `json:"auctions"`
	Lots     int `json:"lots"`
	Sites    int `json:"sites"`
	Hidden   int `json:"hidden"`
	Kept     int `json:"kept"`
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
	if err := h.Events().Publish(r.Context(), "expired.cleaned", "expired", expiredCleaned{
		Auctions: result.Auctions, Lots: result.Lots, Sites: result.Sites, Hidden: result.Hidden,
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
		plan := cleanupPlan(a, now)
		result.Kept += plan.Kept
		if plan.DropAuction {
			if err := p.deleteAuction(ctx, h, a.ID); err != nil {
				return result, err
			}
			result.Auctions++
			continue
		}
		for _, lotID := range plan.DropLots {
			if err := p.deleteLot(ctx, h, a.ID, lotID); err != nil {
				return result, err
			}
			result.Lots++
		}
		if plan.HideAuction && !a.Hidden {
			if _, err := h.Store().Exec(ctx, `UPDATE bidrl_auctions SET hidden = 1 WHERE id = ?`, a.ID); err != nil {
				return result, err
			}
			result.Hidden++
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

// auctionCleanup is what cleanup decided to do with one auction.
type auctionCleanup struct {
	DropAuction bool
	HideAuction bool
	DropLots    []string
	Kept        int
}

// cleanupPlan decides what "Remove ended" removes from one auction.
//
// A saved lot is never removed. The point of saving something is to refer back to it
// later, and later is usually after it has closed: a favourite that disappears on the
// next tidy is worse than no favourite at all. So an ended auction holding a saved lot
// is kept as its shell — the lot keeps its photos, comparable, and location — while its
// unsaved ended lots still go. Explicitly deleting the auction still takes everything,
// saved lots included; that was asked for.
//
// A shell is hidden rather than left in the list. An ended auction that survives a
// tidy still reads as one the tidy missed, so it drops out of the auctions screen and
// stays reachable from the saved lot or the finding that held it. Collecting it again
// brings it back.
//
// An undecided finding pins its lot the same way. A finding is the record of what a
// watchlist turned up, and deleting the lot out from under it before you have looked
// would empty the queue of exactly the things it was built to show you.
func cleanupPlan(a cleanupAuction, now time.Time) auctionCleanup {
	var plan auctionCleanup
	lotEnds := make([]string, 0, len(a.Lots))
	pinned := 0
	for _, lot := range a.Lots {
		lotEnds = append(lotEnds, lot.EndsAt)
		if lot.Favorite || lot.Pending {
			pinned++
		}
	}
	if auctionEnded(a.EndsAt, lotEnds, now) {
		if pinned == 0 {
			plan.DropAuction = true
			return plan
		}
		plan.HideAuction = true
	}
	for _, lot := range a.Lots {
		if lot.Favorite || lot.Pending {
			// A pinned lot counts as kept whenever its auction is over, close time or
			// not: the tidy reporting nothing kept while an auction visibly survived is
			// what made the rule look broken.
			if plan.HideAuction || hasEnded(lot.EndsAt, now) {
				plan.Kept++
			}
			continue
		}
		if hasEnded(lot.EndsAt, now) {
			plan.DropLots = append(plan.DropLots, lot.ID)
		}
	}
	return plan
}

type cleanupAuction struct {
	ID, EndsAt string
	Hidden     bool
	Lots       []cleanupLot
}

type cleanupLot struct {
	ID, EndsAt string
	Favorite   bool
	Pending    bool
}

func (p *Plugin) loadCleanupAuctions(ctx context.Context, h host.Host) ([]cleanupAuction, error) {
	rows, err := h.Store().Query(ctx, `SELECT id, ends_at, hidden FROM bidrl_auctions`)
	if err != nil {
		return nil, err
	}
	var auctions []cleanupAuction
	for rows.Next() {
		var a cleanupAuction
		var hidden int
		if err := rows.Scan(&a.ID, &a.EndsAt, &hidden); err != nil {
			_ = rows.Close()
			return nil, err
		}
		a.Hidden = hidden != 0
		auctions = append(auctions, a)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range auctions {
		lotRows, err := h.Store().Query(ctx, `SELECT l.id, l.ends_at, f.lot_id IS NOT NULL,
			EXISTS (SELECT 1 FROM bidrl_findings d WHERE d.lot_id = l.id AND d.state = 'new')
			FROM bidrl_lots l
			LEFT JOIN bidrl_favorites f ON f.lot_id = l.id
			WHERE l.auction_id = ?`, auctions[i].ID)
		if err != nil {
			return nil, err
		}
		for lotRows.Next() {
			var lot cleanupLot
			var fav, pending int
			if err := lotRows.Scan(&lot.ID, &lot.EndsAt, &fav, &pending); err != nil {
				_ = lotRows.Close()
				return nil, err
			}
			lot.Favorite = fav != 0
			lot.Pending = pending != 0
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
		if _, err := tx.Exec(ctx, `DELETE FROM bidrl_favorites WHERE lot_id IN (SELECT id FROM bidrl_lots WHERE auction_id = ?)`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM bidrl_findings WHERE lot_id IN (SELECT id FROM bidrl_lots WHERE auction_id = ?)`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM bidrl_lot_alerts WHERE lot_id IN (SELECT id FROM bidrl_lots WHERE auction_id = ?)`, id); err != nil {
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
		if _, err := tx.Exec(ctx, `DELETE FROM bidrl_favorites WHERE lot_id = ?`, lotID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM bidrl_findings WHERE lot_id = ?`, lotID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM bidrl_lot_alerts WHERE lot_id = ?`, lotID); err != nil {
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
