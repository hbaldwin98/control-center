package bidrl

import (
	"bytes"
	"errors"
	"fmt"
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
	ID       string
	URL      string
	Title    string
	LotCode  string
	BidCents *int64
	Images   []storedImage
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
	canonical, hostName, auctionID, err := parseAuctionURL(args.URL)
	if err != nil {
		return hostjobs.Permanent(err)
	}
	_ = jc.Logf("collecting auction %s from %s", auctionID, canonical)
	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	if err := p.upsertAuction(jc, h, auctionID, canonical, hostName, "collecting", now); err != nil {
		return err
	}
	if err := jc.Progress(0.05, "opening auction"); err != nil {
		return err
	}

	lots, title, err := p.harvest(jc, h, canonical, hostName, auctionID)
	if err != nil {
		_ = p.failAuction(jc, h, auctionID, err.Error(), now)
		return err
	}
	if title == "" {
		title = "Auction " + auctionID
	}
	if err := p.storeCollected(jc, h, auctionID, canonical, hostName, title, lots, now); err != nil {
		_ = p.failAuction(jc, h, auctionID, err.Error(), now)
		return err
	}
	if err := h.Events().Publish(jc, "auction.collected", auctionID, map[string]any{
		"auctionId": auctionID, "url": canonical, "title": title, "lotCount": len(lots),
	}); err != nil {
		return err
	}
	_ = jc.Logf("stored %d lots for auction %s", len(lots), auctionID)
	return jc.Progress(1, "collected")
}

func (p *Plugin) harvest(jc hostjobs.Context, h host.Host, auctionURL, hostName, auctionID string) ([]collectedLot, string, error) {
	sess, err := h.Browser().Open(jc, hostbrowser.OpenOptions{AllowedHosts: allowedHosts})
	if err != nil {
		return nil, "", err
	}
	defer sess.Close(jc)
	page, err := sess.NewPage(jc)
	if err != nil {
		return nil, "", err
	}
	defer page.Close(jc)

	title, listed, err := p.enumerateLots(jc, page, auctionURL, hostName, auctionID)
	if err != nil {
		return nil, "", err
	}
	if len(listed) == 0 {
		return nil, title, hostjobs.Permanent(fmt.Errorf(
			"bidrl: found no lots on %s: the gallery served no item feed and its markup carries no lot links",
			auctionURL))
	}
	if len(listed) > maxLotsPerAuction {
		_ = p.reject(jc, h, auctionID, "", "lot_cap", fmt.Sprintf("%d lots; keeping first %d", len(listed), maxLotsPerAuction))
		listed = listed[:maxLotsPerAuction]
	}

	out := make([]collectedLot, 0, len(listed))
	for i, raw := range listed {
		if err := jc.Err(); err != nil {
			return nil, "", err
		}
		if err := jc.Progress(0.1+0.8*float64(i)/float64(len(listed)), raw.Title); err != nil {
			return nil, "", err
		}
		lot, err := p.collectOne(jc, h, page, auctionID, raw)
		if err != nil {
			_ = jc.Logf("lot %s failed: %v", raw.URL, err)
			if stopCollect(err) {
				return nil, "", err
			}
			continue
		}
		out = append(out, lot)
	}
	return out, title, nil
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

func (p *Plugin) collectOne(jc hostjobs.Context, h host.Host, page hostbrowser.Page, auctionID string, raw parsedLot) (collectedLot, error) {
	id := lotIDFromPath(raw.URL)
	if id == "" {
		id = blobSafe(raw.URL)
	}
	lot := collectedLot{ID: id, URL: raw.URL, Title: raw.Title, LotCode: raw.LotCode, BidCents: raw.BidCents}
	images := raw.Images
	if len(images) == 0 {
		// Only a scraped lot needs its own page opened; the item feed already carried
		// the title, bid, and photographs.
		if err := page.Goto(jc, raw.URL); err != nil {
			return lot, fmt.Errorf("bidrl: lot page: %w", err)
		}
		_ = page.WaitFor(jc, "body", 15*time.Second)
		html, err := page.Content(jc)
		if err != nil {
			return lot, err
		}
		parsed := parseLotHTML(raw.URL, html)
		if parsed.Title != "" {
			lot.Title = parsed.Title
		}
		if parsed.BidCents != nil {
			lot.BidCents = parsed.BidCents
		}
		if lot.LotCode == "" && len(parsed.Lots) > 0 {
			lot.LotCode = parsed.Lots[0].LotCode
		}
		images = parsed.Images
	}

	var total int64
	for _, imgURL := range images {
		if err := jc.Err(); err != nil {
			return lot, err
		}
		if len(lot.Images) >= maxImagesPerLot {
			_ = p.reject(jc, h, auctionID, id, "image_cap", "more than 12 images")
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
			_ = p.reject(jc, h, auctionID, id, "image_size", fmt.Sprintf("%s is %d bytes", imgURL, len(res.Body)))
			continue
		}
		if total+int64(len(res.Body)) > maxLotImageBytes {
			_ = p.reject(jc, h, auctionID, id, "lot_image_budget", "more than 100 MiB of images")
			break
		}
		key := "auctions/" + blobSafe(auctionID) + "/lots/" + blobSafe(id) + "/" + fmt.Sprintf("%d.%s", len(lot.Images)+1, imageExt(res.MIME))
		ref, err := h.Blobs().Put(jc, key, bytes.NewReader(res.Body), res.MIME)
		if err != nil {
			return lot, fmt.Errorf("bidrl: store image: %w", err)
		}
		lot.Images = append(lot.Images, storedImage{Key: key, URL: imgURL, MIME: res.MIME, Size: ref.Size})
		total += ref.Size
	}
	if lot.Title == "" {
		lot.Title = raw.Title
	}
	if lot.Title == "" {
		lot.Title = "Lot " + id
	}
	return lot, nil
}

func (p *Plugin) upsertAuction(jc hostjobs.Context, h host.Host, id, pageURL, hostName, status, now string) error {
	_, err := h.Store().Exec(jc, `INSERT INTO bidrl_auctions(id, url, title, host, status, created_at)
		VALUES (?, ?, '', ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET url = excluded.url, host = excluded.host, status = excluded.status, last_error = ''`,
		id, pageURL, hostName, status, now)
	return err
}

func (p *Plugin) failAuction(jc hostjobs.Context, h host.Host, id, lastError, now string) error {
	_, err := h.Store().Exec(jc, `UPDATE bidrl_auctions SET status = 'failed', last_error = ?, collected_at = ? WHERE id = ?`,
		lastError, now, id)
	return err
}

func (p *Plugin) storeCollected(jc hostjobs.Context, h host.Host, auctionID, pageURL, hostName, title string, lots []collectedLot, now string) error {
	return h.Store().Tx(jc, func(tx hoststorage.Tx) error {
		if _, err := tx.Exec(jc, `UPDATE bidrl_auctions SET title = ?, url = ?, host = ?, lot_count = ?, status = 'ready', last_error = '', collected_at = ? WHERE id = ?`,
			title, pageURL, hostName, len(lots), now, auctionID); err != nil {
			return err
		}
		for _, lot := range lots {
			if _, err := tx.Exec(jc, `INSERT INTO bidrl_lots(id, auction_id, url, lot_code, title, current_bid_cents, currency, bucket, created_at)
				VALUES (?, ?, ?, ?, ?, ?, 'USD', 'pending', ?)
				ON CONFLICT(id) DO UPDATE SET url = excluded.url, lot_code = excluded.lot_code, title = excluded.title, current_bid_cents = excluded.current_bid_cents`,
				lot.ID, auctionID, lot.URL, lot.LotCode, lot.Title, lot.BidCents, now); err != nil {
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
