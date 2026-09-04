package bidrl

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hostbrowser "github.com/hbaldwin98/control-center/host/browser"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
	hostpolicy "github.com/hbaldwin98/control-center/host/policy"
	hoststorage "github.com/hbaldwin98/control-center/host/storage"
)

type collectArgs struct {
	URL string `json:"url"`
}

type collectedLot struct {
	ID             string
	URL            string
	Title          string
	LotCode        string
	Description    string
	BidCents       *int64
	MinBidCents    *int64
	IncrementCents *int64
	BidCount       int
	HighBidder     string
	HighBidderID   string
	EndsAt         string
	BiddingExt     bool
	ReserveMet     bool
	Images         []storedImage
}

type storedImage struct {
	Key  string
	URL  string
	MIME string
	Size int64
}

func (p *Plugin) collectJob(jc hostjobs.Context) error {
	h, ok := p.host()
	if !ok {
		return fmt.Errorf("bidrl: host is not initialized")
	}
	var args collectArgs
	if err := jc.Args(&args); err != nil {
		return err
	}
	if _, err := p.collectAuction(jc, h, args.URL); err != nil {
		return err
	}
	return jc.Progress(1, "collected")
}

// collectAuction collects one auction and returns how many lots it stored. The
// collect job wraps a single call to it; a sweep calls it once per auction it chose.
func (p *Plugin) collectAuction(jc hostjobs.Context, h host.Host, pageURL string) (int, error) {
	canonical, hostName, auctionID, err := parseAuctionURL(pageURL)
	if err != nil {
		return 0, hostjobs.Permanent(err)
	}
	_ = jc.Logf("collecting auction %s from %s", auctionID, canonical)
	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	if err := p.upsertAuction(jc, h, auctionID, canonical, hostName, "collecting", now); err != nil {
		return 0, err
	}
	if err := jc.Progress(0.05, "opening auction"); err != nil {
		return 0, err
	}

	lots, title, endsAt, err := p.harvest(jc, h, canonical, hostName, auctionID)
	if err != nil {
		_ = p.failAuction(jc, h, auctionID, err.Error(), now)
		return 0, err
	}
	if title == "" {
		title = "Auction " + auctionID
	}
	if err := p.storeCollected(jc, h, auctionID, canonical, hostName, title, lots, endsAt, now); err != nil {
		_ = p.failAuction(jc, h, auctionID, err.Error(), now)
		return 0, err
	}
	loc, err := p.auctionLocation(jc, h, auctionID)
	if err != nil {
		return 0, err
	}
	if err := h.Events().Publish(jc, "auction.collected", auctionID, auctionCollected{
		AuctionID: auctionID, URL: canonical, Title: title, LotCount: len(lots),
		AffiliateID: loc.AffiliateID, AffiliateName: loc.AffiliateName, City: loc.City,
		EndsAt: endsAt,
	}); err != nil {
		return 0, err
	}
	_ = jc.Logf("stored %d lots for auction %s", len(lots), auctionID)
	return len(lots), nil
}

func (p *Plugin) harvest(jc hostjobs.Context, h host.Host, auctionURL, hostName, auctionID string) ([]collectedLot, string, string, error) {
	sess, err := h.Browser().Open(jc, hostbrowser.OpenOptions{AllowedHosts: allowedHosts})
	if err != nil {
		return nil, "", "", err
	}
	defer sess.Close(jc)
	page, err := sess.NewPage(jc)
	if err != nil {
		return nil, "", "", err
	}
	defer page.Close(jc)

	title, listed, err := p.enumerateLots(jc, page, auctionURL, hostName, auctionID)
	if err != nil {
		return nil, "", "", err
	}
	if len(listed) == 0 {
		return nil, title, "", hostjobs.Permanent(fmt.Errorf(
			"bidrl: found no lots on %s: the gallery served no item feed and its markup carries no lot links",
			auctionURL))
	}
	if len(listed) > maxLotsPerAuction {
		_ = p.reject(jc, h, auctionID, "", "lot_cap", fmt.Sprintf("%d lots; keeping first %d", len(listed), maxLotsPerAuction))
		listed = listed[:maxLotsPerAuction]
	}

	out, itemTitle, endsAt, err := p.collectListed(jc, h, page, auctionID, listed)
	if err != nil {
		return nil, "", "", err
	}
	if itemTitle != "" {
		title = itemTitle
	}
	return out, title, endsAt, nil
}

func (p *Plugin) enumerateLots(jc hostjobs.Context, page hostbrowser.Page, auctionURL, hostName, auctionID string) (string, []parsedLot, error) {
	// The first gallery page decides the route: a server-rendered catalog already
	// carries its lot links, while the Angular gallery ships none and delivers lots
	// as a JSON feed instead. Probing on the paged URL rather than the bare gallery
	// keeps this to one navigation, so the only captured feed is the one asked for.
	title, listed, htmlLots, err := p.enumerateViaAPI(jc, page, auctionURL)
	if err != nil {
		return "", nil, err
	}
	if len(listed) > 0 {
		return title, listed, nil
	}
	if len(htmlLots) == 0 {
		_ = jc.Logf("no lots in the item feed; falling back to scraping lot links")
	}
	return p.enumerateViaHTML(jc, page, auctionURL, hostName, auctionID, title)
}

// enumerateViaAPI walks the bid gallery a page at a time and reads the /api/getitems
// responses the Angular app issues. Paging stops once the feed's own total is covered.
// Any lot links found in the served markup are returned separately: their presence means
// this is not the Angular gallery and the caller should scrape instead.
func (p *Plugin) enumerateViaAPI(jc hostjobs.Context, page hostbrowser.Page, auctionURL string) (string, []parsedLot, []parsedLot, error) {
	var (
		title  string
		listed []parsedLot
		seen   = map[string]struct{}{}
		total  int
	)
	for pageNum := 1; pageNum <= maxGalleryPages; pageNum++ {
		if err := jc.Err(); err != nil {
			return "", nil, nil, err
		}
		target := galleryPageURL(auctionURL, pageNum, galleryPerPage)
		_ = jc.Logf("opening %s", target)
		if err := page.Goto(jc, target); err != nil {
			_ = jc.Logf("goto %s: %v", target, err)
			return title, listed, nil, nil
		}
		_ = page.WaitFor(jc, "body", 20*time.Second)
		if pageNum == 1 {
			html, err := page.Content(jc)
			if err != nil {
				return "", nil, nil, err
			}
			parsed := parseAuctionHTML(target, html)
			if len(parsed.Lots) > 0 {
				return parsed.Title, nil, parsed.Lots, nil
			}
			if !looksLikeGalleryApp(html) {
				// No lot links and no gallery app: there is no feed coming, so do not
				// spend the feed timeout waiting for one.
				_ = jc.Logf("%s served neither lot links nor the gallery app", target)
				return parsed.Title, nil, nil, nil
			}
			title = parsed.Title
		}
		lots, feedTitle, feedTotal, err := p.awaitItemsFeed(jc, page, pageNum)
		if err != nil {
			return "", nil, nil, err
		}
		if feedTitle != "" {
			title = feedTitle
		}
		if len(lots) == 0 {
			if pageNum == 1 {
				p.logFeedMiss(jc, page)
			}
			return title, listed, nil, nil
		}
		if feedTotal > total {
			total = feedTotal
		}
		added := 0
		for _, lot := range lots {
			if _, dup := seen[lot.URL]; dup {
				continue
			}
			seen[lot.URL] = struct{}{}
			listed = append(listed, lot)
			added++
		}
		_ = jc.Logf("item feed page %d: %d lots (%d of %d known)", pageNum, added, len(listed), total)
		if added == 0 || len(listed) >= total || len(listed) >= maxLotsPerAuction {
			return title, listed, nil, nil
		}
	}
	return title, listed, nil, nil
}

// awaitItemsFeed polls the captured responses until the feed for this gallery page
// arrives. The app fetches it after load, so the first look is usually too early.
func (p *Plugin) awaitItemsFeed(jc hostjobs.Context, page hostbrowser.Page, want int) ([]parsedLot, string, int, error) {
	deadline := time.Now().Add(feedTimeout)
	for {
		resps, err := page.Responses(jc)
		if err != nil {
			return nil, "", 0, err
		}
		// Newest first: a later page's feed replaces an earlier one at the same URL.
		for i := len(resps) - 1; i >= 0; i-- {
			r := resps[i]
			if r.Status >= 400 || !isGetItemsURL(r.URL) {
				continue
			}
			lots, title, got, total, ok := parseItemsJSON(r.Body)
			if !ok || (got != 0 && got != want) {
				continue
			}
			return lots, title, total, nil
		}
		if err := jc.Err(); err != nil {
			return nil, "", 0, err
		}
		if !time.Now().Before(deadline) {
			return nil, "", 0, nil
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// logFeedMiss names what the session did capture. A gallery that renders but yields no
// item feed almost always means JavaScript never ran — the in-process fake browser
// engine, which serves fixtures rather than the real site.
func (p *Plugin) logFeedMiss(jc hostjobs.Context, page hostbrowser.Page) {
	resps, err := page.Responses(jc)
	if err != nil {
		_ = jc.Logf("item feed missing and captured responses unavailable: %v", err)
		return
	}
	if len(resps) == 0 {
		_ = jc.Logf("the gallery made no JSON requests at all; the browser engine is probably " +
			"'fake', which serves in-process fixtures and runs no JavaScript — set browser.engine " +
			"to playwright (or CC_BROWSER_ENGINE=playwright)")
		return
	}
	urls := make([]string, 0, len(resps))
	for _, r := range resps {
		urls = append(urls, fmt.Sprintf("%d %s", r.Status, r.URL))
	}
	_ = jc.Logf("no %s response among %d captured: %s", getItemsPath, len(resps), strings.Join(urls, ", "))
}

func (p *Plugin) enumerateViaHTML(jc hostjobs.Context, page hostbrowser.Page, auctionURL, hostName, auctionID, title string) (string, []parsedLot, error) {
	targets := unique([]string{auctionURL, bidgalleryURL(auctionURL), printCatalogURL(hostName, auctionID)})
	var listed []parsedLot
	seen := map[string]struct{}{}
	for _, target := range targets {
		if jc.Err() != nil {
			return "", nil, jc.Err()
		}
		_ = jc.Logf("opening %s", target)
		if err := page.Goto(jc, target); err != nil {
			_ = jc.Logf("goto %s: %v", target, err)
			continue
		}
		_ = page.WaitFor(jc, "body", 20*time.Second)
		html, err := page.Content(jc)
		if err != nil {
			return "", nil, err
		}
		parsed := parseAuctionHTML(target, html)
		if parsed.Title != "" && title == "" {
			title = parsed.Title
		}
		for _, lot := range parsed.Lots {
			if _, ok := seen[lot.URL]; ok {
				continue
			}
			seen[lot.URL] = struct{}{}
			listed = append(listed, lot)
		}
		if len(listed) > 0 && !strings.Contains(target, "print_catalog") {
			break
		}
	}
	return title, listed, nil
}

func (p *Plugin) collectListed(jc hostjobs.Context, h host.Host, page hostbrowser.Page, auctionID string, listed []parsedLot) ([]collectedLot, string, string, error) {
	pace := newPacer(bidrlMinInterval)
	out := make([]collectedLot, 0, len(listed))
	var title, endsAt string
	for i, raw := range listed {
		if err := jc.Err(); err != nil {
			return nil, "", "", err
		}
		itemID := strings.TrimSpace(raw.ItemID)
		if itemID == "" {
			itemID = lotIDFromPath(raw.URL)
		}
		aid := strings.TrimSpace(raw.AuctionID)
		if aid == "" {
			aid = auctionID
		}
		label := raw.Title
		if label == "" {
			label = itemID
		}
		if err := jc.Progress(0.1+0.8*float64(i)/float64(len(listed)), label); err != nil {
			return nil, "", "", err
		}
		rec, err := p.fetchItemData(jc, page, pace, aid, itemID)
		if err != nil {
			_ = jc.Logf("ItemData %s/%s: %v", aid, itemID, err)
			if stopCollect(err) {
				return nil, "", "", err
			}
			continue
		}
		if title == "" && rec.AuctionTitle != "" {
			title = rec.AuctionTitle
		}
		if rec.EndsAt != "" && rec.EndsAt > endsAt {
			endsAt = rec.EndsAt
		}
		lot := rec.toCollected(itemID, aid, raw)
		photos := rec.Images
		if len(photos) == 0 {
			photos = raw.Images
		}
		imgs, err := p.downloadPhotos(jc, h, page, auctionID, lot.ID, photos)
		if err != nil {
			_ = jc.Logf("photos %s: %v", lot.ID, err)
			if stopCollect(err) {
				return nil, "", "", err
			}
		} else {
			lot.Images = imgs
		}
		out = append(out, lot)
	}
	return out, title, endsAt, nil
}

func (rec itemRecord) toCollected(itemID, auctionID string, raw parsedLot) collectedLot {
	id := rec.ItemID
	if id == "" {
		id = itemID
	}
	lotURL := rec.URL
	if lotURL == "" {
		lotURL = raw.URL
	}
	title := rec.Title
	if title == "" {
		title = raw.Title
	}
	if title == "" {
		title = "Lot " + id
	}
	code := rec.LotCode
	if code == "" {
		code = raw.LotCode
	}
	bid := rec.BidCents
	if bid == nil {
		bid = raw.BidCents
	}
	return collectedLot{
		ID: id, URL: lotURL, Title: title, LotCode: code, Description: rec.Description,
		BidCents: bid, MinBidCents: rec.MinBidCents, IncrementCents: rec.IncrementCents,
		BidCount: rec.BidCount, HighBidder: rec.HighBidder, HighBidderID: rec.HighBidderID, EndsAt: rec.EndsAt,
		BiddingExt: rec.BiddingExt, ReserveMet: rec.ReserveMet,
	}
}

func (p *Plugin) fetchItemData(jc hostjobs.Context, page hostbrowser.Page, pace *pacer, auctionID, itemID string) (itemRecord, error) {
	if itemID == "" || auctionID == "" {
		return itemRecord{}, fmt.Errorf("bidrl: ItemData needs auction and item id")
	}
	if err := pace.wait(jc); err != nil {
		return itemRecord{}, err
	}
	res, err := page.Post(jc, itemDataURL(), url.Values{"item_id": {itemID}, "auction_id": {auctionID}})
	if err != nil {
		return itemRecord{}, err
	}
	if err := pace.observe(jc, res.Status); err != nil {
		return itemRecord{}, err
	}
	if res.Status >= 400 {
		return itemRecord{}, fmt.Errorf("bidrl: ItemData HTTP %d", res.Status)
	}
	rec, ok := parseItemData(auctionID, itemID, res.Body)
	if !ok {
		return itemRecord{}, fmt.Errorf("bidrl: unreadable ItemData for item %s", itemID)
	}
	return rec, nil
}

func (p *Plugin) downloadPhotos(jc hostjobs.Context, h host.Host, page hostbrowser.Page, auctionID, lotID string, images []string) ([]storedImage, error) {
	var (
		out   []storedImage
		total int64
	)
	for _, imgURL := range images {
		if err := jc.Err(); err != nil {
			return out, err
		}
		if len(out) >= maxImagesPerLot {
			_ = p.reject(jc, h, auctionID, lotID, "image_cap", "more than 12 images")
			break
		}
		res, err := page.Get(jc, imgURL)
		if err != nil {
			_ = jc.Logf("image %s: %v", imgURL, err)
			continue
		}
		if !imageMIME(res.MIME) {
			continue
		}
		if int64(len(res.Body)) > maxImageBytes {
			_ = p.reject(jc, h, auctionID, lotID, "image_size", fmt.Sprintf("%s is %d bytes", imgURL, len(res.Body)))
			continue
		}
		if total+int64(len(res.Body)) > maxLotImageBytes {
			_ = p.reject(jc, h, auctionID, lotID, "lot_image_budget", "more than 100 MiB of images")
			break
		}
		key := "auctions/" + blobSafe(auctionID) + "/lots/" + blobSafe(lotID) + "/" + fmt.Sprintf("%d.%s", len(out)+1, imageExt(res.MIME))
		ref, err := h.Blobs().Put(jc, key, bytes.NewReader(res.Body), res.MIME)
		if err != nil {
			return out, fmt.Errorf("bidrl: store image: %w", err)
		}
		out = append(out, storedImage{Key: key, URL: imgURL, MIME: res.MIME, Size: ref.Size})
		total += ref.Size
	}
	return out, nil
}

func (p *Plugin) upsertAuction(jc hostjobs.Context, h host.Host, id, pageURL, hostName, status, now string) error {
	_, err := h.Store().Exec(jc, `INSERT INTO bidrl_auctions(id, url, title, host, status, created_at)
		VALUES (?, ?, '', ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET url = excluded.url, host = excluded.host, status = excluded.status, last_error = '',
			-- Asking to collect it again is asking to see it again.
			hidden = 0`,
		id, pageURL, hostName, status, now)
	return err
}

func (p *Plugin) failAuction(jc hostjobs.Context, h host.Host, id, lastError, now string) error {
	_, err := h.Store().Exec(jc, `UPDATE bidrl_auctions SET status = 'failed', last_error = ?, collected_at = ? WHERE id = ?`,
		lastError, now, id)
	return err
}

func (p *Plugin) storeCollected(jc hostjobs.Context, h host.Host, auctionID, pageURL, hostName, title string, lots []collectedLot, endsAt, now string) error {
	return h.Store().Tx(jc, func(tx hoststorage.Tx) error {
		if _, err := tx.Exec(jc, `UPDATE bidrl_auctions SET title = ?, url = ?, host = ?, lot_count = ?, status = 'ready', last_error = '', collected_at = ?, ends_at = ? WHERE id = ?`,
			title, pageURL, hostName, len(lots), now, endsAt, auctionID); err != nil {
			return err
		}
		if err := stampAuctionLocation(jc, tx, auctionID); err != nil {
			return err
		}
		for _, lot := range lots {
			ext, reserve := 0, 0
			if lot.BiddingExt {
				ext = 1
			}
			if lot.ReserveMet {
				reserve = 1
			}
			if _, err := tx.Exec(jc, `INSERT INTO bidrl_lots(id, auction_id, url, lot_code, title, current_bid_cents, currency, bucket, created_at, ends_at, bid_count, high_bidder, high_bidder_id, min_bid_cents, bid_increment_cents, bidding_extended, reserve_met, description, itemdata_at)
				VALUES (?, ?, ?, ?, ?, ?, 'USD', 'pending', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(id) DO UPDATE SET url = excluded.url, lot_code = excluded.lot_code, title = excluded.title, current_bid_cents = excluded.current_bid_cents,
					ends_at = excluded.ends_at, bid_count = excluded.bid_count, high_bidder = excluded.high_bidder, high_bidder_id = excluded.high_bidder_id, min_bid_cents = excluded.min_bid_cents,
					bid_increment_cents = excluded.bid_increment_cents, bidding_extended = excluded.bidding_extended, reserve_met = excluded.reserve_met,
					description = excluded.description, itemdata_at = excluded.itemdata_at`,
				lot.ID, auctionID, lot.URL, lot.LotCode, lot.Title, lot.BidCents, now, lot.EndsAt, lot.BidCount, lot.HighBidder,
				lot.HighBidderID, lot.MinBidCents, lot.IncrementCents, ext, reserve, lot.Description, now); err != nil {
				return err
			}
			if _, err := tx.Exec(jc, `DELETE FROM bidrl_images WHERE lot_id = ?`, lot.ID); err != nil {
				return err
			}
			for i, img := range lot.Images {
				if _, err := tx.Exec(jc, `INSERT INTO bidrl_images(lot_id, ordinal, blob_key, source_url, mime, size_bytes)
					VALUES (?, ?, ?, ?, ?, ?)`, lot.ID, i+1, img.Key, img.URL, img.MIME, img.Size); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (p *Plugin) reject(jc hostjobs.Context, h host.Host, auctionID, lotID, reason, detail string) error {
	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	_, err := h.Store().Exec(jc, `INSERT INTO bidrl_rejections(auction_id, lot_id, reason, detail, created_at) VALUES (?, ?, ?, ?, ?)`,
		auctionID, lotID, reason, detail, now)
	return err
}

func stopCollect(err error) bool {
	return errors.Is(err, hostpolicy.ErrPluginDisabled) || errors.Is(err, hostpolicy.ErrBudgetExceeded)
}

func unique(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// stampAuctionLocation copies the auction's SITES location onto its own row.
//
// Location has to be stored, not joined at read time: discover clears
// bidrl_affiliate_auctions on every run, so an auction that reads its location
// from that cache loses it the moment it closes or drops off its landing page,
// and ruling a lot out by drive time is exactly what you want to still work
// after the auction is over. A cache miss leaves the stored value alone rather
// than blanking it — the next discover backfills it.
// auctionLocation reads back the location stamped onto a collected auction, so an
// auction.collected event can say where the auction is and not only what it is called.
type auctionLocation struct {
	AffiliateID   string
	AffiliateName string
	City          string
}

func (p *Plugin) auctionLocation(jc hostjobs.Context, h host.Host, auctionID string) (auctionLocation, error) {
	var loc auctionLocation
	row := h.Store().QueryRow(jc, `SELECT affiliate_id, affiliate_name, city FROM bidrl_auctions WHERE id = ?`, auctionID)
	if err := row.Scan(&loc.AffiliateID, &loc.AffiliateName, &loc.City); err != nil {
		return auctionLocation{}, err
	}
	return loc, nil
}

func stampAuctionLocation(jc hostjobs.Context, tx hoststorage.Tx, auctionID string) error {
	_, err := tx.Exec(jc, `UPDATE bidrl_auctions SET
			affiliate_id   = IFNULL((SELECT affiliate_id   FROM bidrl_affiliate_auctions WHERE id = ?), affiliate_id),
			affiliate_name = IFNULL((SELECT affiliate_name FROM bidrl_affiliate_auctions WHERE id = ?), affiliate_name),
			city           = IFNULL((SELECT city           FROM bidrl_affiliate_auctions WHERE id = ?), city)
		WHERE id = ?`, auctionID, auctionID, auctionID, auctionID)
	return err
}
