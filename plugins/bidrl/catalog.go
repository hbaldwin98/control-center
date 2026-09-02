package bidrl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hostbrowser "github.com/hbaldwin98/control-center/host/browser"
)

// The catalog endpoint answers for a whole auction what the pusher snapshot answers for
// one lot. A 444-lot auction is one 2 MB POST in well under a second; the same refresh
// lot by lot is 444 requests spaced by the pacer, which is minutes of origin traffic
// for data the site was willing to hand over in a single response.
//
// So this is the bid path: one call per auction, not one per lot. The per-lot snapshot
// survives only where it is genuinely one lot -- the single-lot view.
const (
	// catalogPerPage is BidRL's own maximum page size. Every auction seen so far fits
	// in one page; the fetch still follows total_pages rather than assuming that.
	catalogPerPage = 500
	// catalogMaxPages bounds a hostile or broken total_pages.
	catalogMaxPages = 8
)

func catalogURL() string { return "https://www.bidrl.com" + getItemsPath }

// catalogBids is one auction's bid state, keyed by lot id.
type catalogBids struct {
	Snapshots map[string]catalogSnapshot
	Total     int
	Pages     int
}

// catalogSnapshot is a pusherSnapshot plus the numeric bidder id. The catalog names the
// high bidder only by id, while the feed and ItemData carry the username, so the id is
// what tells us whether a stored username still belongs to whoever is winning.
type catalogSnapshot struct {
	pusherSnapshot
	HighBidderID string
}

// parseCatalogBids reads the bid fields off one /api/getitems body. It shares the
// snapshot type with the feed and the per-lot poll, so all three write the same columns
// the same way, and it honours each item's own time_offset instead of the constant the
// pusher payload forces us to assume.
func parseCatalogBids(body []byte) (catalogBids, bool) {
	var resp struct {
		Total      json.Number       `json:"total"`
		TotalPages json.Number       `json:"total_pages"`
		Items      []json.RawMessage `json:"items"`
	}
	if json.Unmarshal(body, &resp) != nil || len(resp.Items) == 0 {
		return catalogBids{}, false
	}
	out := catalogBids{
		Snapshots: make(map[string]catalogSnapshot, len(resp.Items)),
		Total:     atoiNumber(resp.Total),
		Pages:     atoiNumber(resp.TotalPages),
	}
	for _, raw := range resp.Items {
		item := asObject(raw)
		if len(item) == 0 {
			continue
		}
		id := strings.TrimSpace(firstString(item, "id", "item_id"))
		if id == "" {
			continue
		}
		out.Snapshots[id] = catalogSnapshot{
			pusherSnapshot: pusherSnapshot{
				BidCents:       firstMoney(item, "current_bid", "currentBid"),
				MinBidCents:    firstMoney(item, "minimum_bid", "min_bid"),
				IncrementCents: firstMoney(item, "current_increment", "bid_increment", "increment"),
				BidCount:       firstInt(item, "bid_count", "bids"),
				HighBidder:     truncateRunes(strings.TrimSpace(firstString(item, "highbidder_username", "high_bidder_username")), 80),
				EndsAt:         parseEndTime(firstRaw(item, "end_time", "ends_at"), itemTimeOffset(item)),
				BiddingExt:     firstBool(item, "bidding_extended"),
				ReserveMet:     firstBool(item, "reserve_met"),
			},
			HighBidderID: truncateRunes(strings.TrimSpace(firstString(item, "high_bidder")), 80),
		}
	}
	if len(out.Snapshots) == 0 {
		return catalogBids{}, false
	}
	return out, true
}

// fetchCatalogBids reads every open lot's bid state for one auction. It uses the direct
// allowlisted request rather than a browser session: this is a plain form POST to a
// JSON API, so it needs no engine, no cookie jar, and none of the plugin's scarce
// session slots.
func (p *Plugin) fetchCatalogBids(ctx context.Context, h host.Host, pace *pacer, auctionID string) (catalogBids, error) {
	if strings.TrimSpace(auctionID) == "" {
		return catalogBids{}, fmt.Errorf("bidrl: catalog needs an auction id")
	}
	all := catalogBids{Snapshots: map[string]catalogSnapshot{}}
	for page := 1; page <= catalogMaxPages; page++ {
		if err := ctx.Err(); err != nil {
			return all, err
		}
		if pace != nil {
			if err := pace.wait(ctx); err != nil {
				return all, err
			}
		}
		form := url.Values{
			"auction_id":       {auctionID},
			"filters[perpage]": {strconv.Itoa(catalogPerPage)},
			"filters[page]":    {strconv.Itoa(page)},
			"show_closed":      {"0"},
		}
		// Do's body is bytes; the form encoding is declared by the header the same way
		// the browser session's Post declares it.
		res, err := h.Browser().Do(ctx, hostbrowser.OpenOptions{AllowedHosts: allowedHosts}, hostbrowser.Request{
			Method:  "POST",
			URL:     catalogURL(),
			Headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Accept": "application/json"},
			Body:    []byte(form.Encode()),
		})
		if err != nil {
			return all, err
		}
		if pace != nil {
			if err := pace.observe(ctx, res.Status); err != nil {
				return all, err
			}
		}
		if res.Status >= 400 {
			return all, fmt.Errorf("bidrl: catalog HTTP %d", res.Status)
		}
		bids, ok := parseCatalogBids(res.Body)
		if !ok {
			if page == 1 {
				return all, fmt.Errorf("bidrl: unreadable catalog for auction %s", auctionID)
			}
			break
		}
		for id, snap := range bids.Snapshots {
			all.Snapshots[id] = snap
		}
		all.Total = bids.Total
		all.Pages = bids.Pages
		if bids.Pages <= page || len(bids.Snapshots) < catalogPerPage {
			break
		}
	}
	return all, nil
}

// applyCatalogBids writes one auction's snapshot onto its lots and returns how many
// rows moved.
//
// The high bidder is the one field the catalog cannot fully answer: it names the winner
// by id, never by username. So a stored name survives only while the id confirms it is
// still the same person. When the id has changed -- or when we have no id yet to
// compare, which is every lot's first catalog refresh -- the name is cleared rather
// than left describing someone who was outbid. The feed and ItemData both carry the
// username, so the name comes back the moment either of them sees the lot.
func (p *Plugin) applyCatalogBids(ctx context.Context, h host.Host, auctionID string, bids catalogBids) (int, error) {
	if len(bids.Snapshots) == 0 {
		return 0, nil
	}
	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	changed := 0
	endsAt := ""
	for id, snap := range bids.Snapshots {
		if err := ctx.Err(); err != nil {
			return changed, err
		}
		ext, reserve := 0, 0
		if snap.BiddingExt {
			ext = 1
		}
		if snap.ReserveMet {
			reserve = 1
		}
		res, err := h.Store().Exec(ctx, `UPDATE bidrl_lots SET current_bid_cents = ?, min_bid_cents = ?,
			bid_increment_cents = ?, bid_count = ?,
			high_bidder = CASE
				WHEN ? != '' THEN ?
				WHEN ? != '' AND high_bidder_id = ? THEN high_bidder
				ELSE '' END,
			high_bidder_id = CASE WHEN ? != '' THEN ? ELSE high_bidder_id END,
			ends_at = COALESCE(NULLIF(?, ''), ends_at), bidding_extended = ?, reserve_met = ?,
			bids_refreshed_at = ? WHERE id = ? AND auction_id = ?`,
			snap.BidCents, snap.MinBidCents, snap.IncrementCents, snap.BidCount,
			snap.HighBidder, snap.HighBidder, snap.HighBidderID, snap.HighBidderID,
			snap.HighBidderID, snap.HighBidderID,
			snap.EndsAt, ext, reserve, now, id, auctionID)
		if err != nil {
			return changed, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return changed, err
		}
		if n > 0 {
			changed++
		}
		if snap.EndsAt > endsAt {
			endsAt = snap.EndsAt
		}
	}
	if endsAt != "" {
		if _, err := h.Store().Exec(ctx, `UPDATE bidrl_auctions SET ends_at = ? WHERE id = ?`, endsAt, auctionID); err != nil {
			return changed, err
		}
	}
	return changed, nil
}

// Freshness on open.
//
// A screen that shows bids should show what they are, not what they were the last time
// somebody pressed refresh. Every list view therefore re-reads the catalog for the
// auctions it is about to render, before it renders them -- one request per auction,
// which is the only reason this is affordable at page-load time.
//
// It stays inside a budget rather than being a job, because a page load that waits on
// an origin has to stay a page load: a TTL skips auctions refreshed moments ago, a cap
// bounds how many auctions one screen may fetch, and a deadline gives up and serves
// what is stored. Nothing here can fail a request -- the rows are already there.
const (
	catalogFreshTTL   = 30 * time.Second
	catalogMaxPerView = 3
	catalogViewBudget = 2500 * time.Millisecond
)

// freshenAuctions re-reads the catalog for auctions whose bids are older than the TTL,
// soonest-closing first. It returns how many auctions it refreshed.
func (p *Plugin) freshenAuctions(ctx context.Context, h host.Host, auctionIDs []string) int {
	ids := p.staleAuctions(ctx, h, dedupeIDs(auctionIDs))
	if len(ids) == 0 {
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, catalogViewBudget)
	defer cancel()

	pace := newPacer(bidrlMinInterval)
	done := 0
	for _, id := range ids {
		bids, err := p.fetchCatalogBids(ctx, h, pace, id)
		if err != nil {
			// A page load never fails on this: the stored rows are still served, and
			// the next open or the live feed will catch up.
			break
		}
		if _, err := p.applyCatalogBids(ctx, h, id, bids); err != nil {
			break
		}
		done++
	}
	return done
}

// staleAuctions keeps the auctions that are open, actually stale, and closest to
// closing -- which is where a wrong price matters most -- capped at what one view may
// fetch.
//
// Staleness is the auction's *stalest* open lot, not its freshest: one lot refreshed a
// moment ago by its own screen says nothing about the two hundred beside it. Closed
// lots are excluded from the question entirely, since their prices cannot move and
// their timestamps would otherwise keep an auction permanently stale.
func (p *Plugin) staleAuctions(ctx context.Context, h host.Host, ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	now := h.Clock().Now().UTC()
	args := make([]any, 0, len(ids)+3)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args,
		now.Format(time.RFC3339Nano),
		now.Add(-catalogFreshTTL).Format(time.RFC3339Nano),
		catalogMaxPerView)
	rows, err := h.Store().Query(ctx, `SELECT auction_id, MIN(NULLIF(ends_at, '')) AS closes
		FROM bidrl_lots
		WHERE auction_id IN (`+placeholders(len(ids))+`)
		AND (ends_at IS NULL OR ends_at = '' OR ends_at > ?)
		GROUP BY auction_id
		HAVING MIN(bids_refreshed_at) < ?
		ORDER BY closes IS NULL, closes
		LIMIT ?`, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		var closes any
		if err := rows.Scan(&id, &closes); err != nil {
			return out
		}
		out = append(out, id)
	}
	return out
}

// freshenLots re-reads the catalog for the auctions a rendered list touches, and
// reports whether anything moved, in which case the caller re-queries so it serves the
// prices it just fetched rather than the ones it read a moment earlier.
func (p *Plugin) freshenLots(ctx context.Context, h host.Host, lots []lotView) bool {
	return p.freshenAuctions(ctx, h, auctionIDsOf(lots)) > 0
}

// auctionIDsOf is the set of auctions a rendered page touches.
func auctionIDsOf(lots []lotView) []string {
	out := make([]string, 0, len(lots))
	for _, l := range lots {
		if l.AuctionID != "" {
			out = append(out, l.AuctionID)
		}
	}
	return out
}

// One lot, one request.
//
// The lot screen is the one place where the per-lot snapshot is the right endpoint: a
// single lot's price is a few hundred bytes there, against two megabytes for its whole
// auction. It is the same trade as the catalog, read the other way round.

// fetchLotBid reads one lot's bid state. Like the catalog fetch it goes direct rather
// than through a browser session, so it can serve a page load.
func (p *Plugin) fetchLotBid(ctx context.Context, h host.Host, pace *pacer, auctionID, itemID string) (pusherSnapshot, error) {
	if strings.TrimSpace(auctionID) == "" || strings.TrimSpace(itemID) == "" {
		return pusherSnapshot{}, fmt.Errorf("bidrl: lot snapshot needs an auction and item id")
	}
	if pace != nil {
		if err := pace.wait(ctx); err != nil {
			return pusherSnapshot{}, err
		}
	}
	res, err := h.Browser().Do(ctx, hostbrowser.OpenOptions{AllowedHosts: allowedHosts}, hostbrowser.Request{
		Method:  "GET",
		URL:     pusherURL(auctionID, itemID),
		Headers: map[string]string{"Accept": "application/json"},
	})
	if err != nil {
		return pusherSnapshot{}, err
	}
	if pace != nil {
		if err := pace.observe(ctx, res.Status); err != nil {
			return pusherSnapshot{}, err
		}
	}
	if res.Status >= 400 {
		return pusherSnapshot{}, fmt.Errorf("bidrl: pusher HTTP %d", res.Status)
	}
	snap, ok := parsePusher(res.Body)
	if !ok {
		return pusherSnapshot{}, fmt.Errorf("bidrl: unreadable pusher for item %s", itemID)
	}
	return snap, nil
}

// freshenLot re-reads one lot before its screen renders, on the same terms as the list
// views: a TTL so a screen revalidating on every bid event cannot turn into a request
// per event, and a failure that serves what is stored rather than failing the page.
func (p *Plugin) freshenLot(ctx context.Context, h host.Host, lotID string) {
	var auctionID, refreshedAt, endsAt string
	if err := h.Store().QueryRow(ctx, `SELECT auction_id, bids_refreshed_at, ends_at
		FROM bidrl_lots WHERE id = ?`, lotID).Scan(&auctionID, &refreshedAt, &endsAt); err != nil {
		return
	}
	now := h.Clock().Now().UTC()
	if refreshedAt >= now.Add(-catalogFreshTTL).Format(time.RFC3339Nano) {
		return
	}
	// A closed lot's price cannot move, so there is nothing to be current about.
	if endsAt != "" && endsAt <= now.Format(time.RFC3339Nano) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, catalogViewBudget)
	defer cancel()
	snap, err := p.fetchLotBid(ctx, h, newPacer(bidrlMinInterval), auctionID, lotID)
	if err != nil {
		return
	}
	ext, reserve := 0, 0
	if snap.BiddingExt {
		ext = 1
	}
	if snap.ReserveMet {
		reserve = 1
	}
	// The snapshot carries the username, so unlike the catalog it can name the winner
	// outright rather than reasoning about ids.
	if _, err := h.Store().Exec(ctx, `UPDATE bidrl_lots SET current_bid_cents = ?, min_bid_cents = ?,
		bid_increment_cents = ?, bid_count = ?, high_bidder = ?, high_bidder_id = ?,
		ends_at = COALESCE(NULLIF(?, ''), ends_at), bidding_extended = ?, reserve_met = ?,
		bids_refreshed_at = ? WHERE id = ?`,
		snap.BidCents, snap.MinBidCents, snap.IncrementCents, snap.BidCount, snap.HighBidder,
		snap.HighBidderID, snap.EndsAt, ext, reserve, now.Format(time.RFC3339Nano), lotID); err != nil {
		return
	}
	if snap.EndsAt != "" && auctionID != "" {
		_, _ = h.Store().Exec(ctx, `UPDATE bidrl_auctions SET ends_at = ?
			WHERE id = ? AND (ends_at IS NULL OR ends_at < ?)`, snap.EndsAt, auctionID, snap.EndsAt)
	}
}

// dedupeIDs keeps the caller's order, drops blanks and repeats, and bounds the list.
func dedupeIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
		if len(out) >= maxLiveLots {
			break
		}
	}
	return out
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
