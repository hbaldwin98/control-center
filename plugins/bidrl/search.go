package bidrl

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hostbrowser "github.com/hbaldwin98/control-center/host/browser"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
	hoststorage "github.com/hbaldwin98/control-center/host/storage"
)

const (
	maxSearchHits  = 80
	maxSearchPages = 3
	keepSearches   = 10
)

type searchArgs struct {
	SearchID string `json:"searchId"`
	Query    string `json:"query"`
	Scope    string `json:"scope"`
}

type searchHit struct {
	LotID         string
	AuctionID     string
	URL           string
	Title         string
	AuctionTitle  string
	LotCode       string
	AffiliateID   string
	AffiliateName string
	Preferred     bool
	BidCents      *int64
	Score         float64
	Reason        string
	Source        string
}

func (p *Plugin) searchJob(jc hostjobs.Context) error {
	h, ok := p.host()
	if !ok {
		return fmt.Errorf("bidrl: host is not initialized")
	}
	var args searchArgs
	if err := jc.Args(&args); err != nil {
		return err
	}
	q := strings.Join(strings.Fields(args.Query), " ")
	if q == "" {
		return hostjobs.Permanent(fmt.Errorf("bidrl: search needs a query"))
	}
	cfg := p.cfg()
	if args.Scope == scopePrefer || args.Scope == scopeOnly || args.Scope == scopeAll {
		cfg.SearchScope = args.Scope
	}
	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	if err := p.markSearch(jc, h, args.SearchID, q, cfg.SearchScope, "running", 0, "", now); err != nil {
		return err
	}

	sess, err := h.Browser().Open(jc, hostbrowser.OpenOptions{AllowedHosts: allowedHosts})
	if err != nil {
		_ = p.markSearch(jc, h, args.SearchID, q, cfg.SearchScope, "failed", 0, err.Error(), now)
		return err
	}
	defer sess.Close(jc)
	page, err := sess.NewPage(jc)
	if err != nil {
		return err
	}
	defer page.Close(jc)

	_ = jc.Progress(0.08, "finding SITES auctions")
	if _, err := p.discoverSites(jc, h, page); err != nil {
		_ = jc.Logf("SITES discover: %v", err)
	}
	preferred, err := p.preferredAuctionIDs(jc, h)
	if err != nil {
		return err
	}

	variants := expandQuery(q)
	_ = jc.Logf("search %q → %s", q, strings.Join(variants, " | "))

	_ = jc.Progress(0.35, "matching collected lots")
	local, err := p.searchLocal(jc, h, q, preferred)
	if err != nil {
		return err
	}

	_ = jc.Progress(0.5, "searching open auctions")
	live, err := p.searchLive(jc, page, variants)
	if err != nil {
		_ = jc.Logf("live search: %v", err)
	}

	hits := mergeHits(q, cfg, local, live, preferred)
	if err := p.storeHits(jc, h, args.SearchID, hits); err != nil {
		_ = p.markSearch(jc, h, args.SearchID, q, cfg.SearchScope, "failed", 0, err.Error(), now)
		return err
	}
	if err := h.Events().Publish(jc, "search.completed", args.SearchID, map[string]any{
		"query": q, "scope": cfg.SearchScope, "hits": len(hits),
	}); err != nil {
		return err
	}
	return jc.Progress(1, fmt.Sprintf("%d hits", len(hits)))
}

func (p *Plugin) searchLocal(jc hostjobs.Context, h host.Host, query string, preferred map[string]landingAuction) ([]searchHit, error) {
	rows, err := h.Store().Query(jc, `SELECT l.id, l.auction_id, l.url, l.lot_code, l.title, l.current_bid_cents,
		IFNULL(a.identification,''), IFNULL(a.model_or_sku,''), IFNULL(a.category,''), IFNULL(a.search_terms,''), IFNULL(a.notes,''), IFNULL(auc.title,''), IFNULL(s.affiliate_id,''), IFNULL(s.affiliate_name,'')
		FROM bidrl_lots l
		LEFT JOIN bidrl_analyses a ON a.id = (SELECT MAX(id) FROM bidrl_analyses WHERE lot_id = l.id)
		LEFT JOIN bidrl_auctions auc ON auc.id = l.auction_id
		LEFT JOIN bidrl_affiliate_auctions s ON s.id = l.auction_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []searchHit
	for rows.Next() {
		var (
			hit                                                searchHit
			ident, model, category, terms, notes, affID, affName string
		)
		if err := rows.Scan(&hit.LotID, &hit.AuctionID, &hit.URL, &hit.LotCode, &hit.Title, &hit.BidCents,
			&ident, &model, &category, &terms, &notes, &hit.AuctionTitle, &affID, &affName); err != nil {
			return nil, err
		}
		score, reason := matchScore(query, hit.Title, ident, model, category, notesAndTerms(terms, notes))
		if score <= 0 {
			continue
		}
		if loc, ok := preferred[hit.AuctionID]; ok {
			hit.Preferred = true
			hit.AffiliateID = loc.Affiliate
			hit.AffiliateName = loc.Name
		} else {
			hit.AffiliateID = affID
			hit.AffiliateName = affName
		}
		hit.Score = score
		hit.Reason = reason
		hit.Source = "local"
		out = append(out, hit)
	}
	return out, rows.Err()
}

func (p *Plugin) searchLive(jc hostjobs.Context, page hostbrowser.Page, variants []string) ([]parsedLot, error) {
	seen := map[string]struct{}{}
	var out []parsedLot
	for i, q := range variants {
		if err := jc.Err(); err != nil {
			return nil, err
		}
		for pageNum := 1; pageNum <= maxSearchPages; pageNum++ {
			target := allitemsURL(q, pageNum, galleryPerPage)
			_ = jc.Logf("opening %s", target)
			if err := page.Goto(jc, target); err != nil {
				_ = jc.Logf("goto %s: %v", target, err)
				break
			}
			_ = page.WaitFor(jc, "body", 20*time.Second)
			lots, err := p.lotsOnSearchPage(jc, page, target, pageNum)
			if err != nil {
				return nil, err
			}
			added := 0
			for _, lot := range lots {
				if _, dup := seen[lot.URL]; dup {
					continue
				}
				seen[lot.URL] = struct{}{}
				out = append(out, lot)
				added++
			}
			_ = jc.Logf("search %q page %d: %d new lots", q, pageNum, added)
			if added == 0 || len(out) >= maxSearchHits {
				break
			}
		}
		_ = jc.Progress(0.5+0.35*float64(i+1)/float64(max(1, len(variants))), q)
		if len(out) >= maxSearchHits {
			break
		}
	}
	if len(out) > maxSearchHits {
		out = out[:maxSearchHits]
	}
	return out, nil
}

func (p *Plugin) lotsOnSearchPage(jc hostjobs.Context, page hostbrowser.Page, pageURL string, pageNum int) ([]parsedLot, error) {
	html, err := page.Content(jc)
	if err != nil {
		return nil, err
	}
	scraped := parseAuctionHTML(pageURL, html)
	if len(scraped.Lots) > 0 {
		return scraped.Lots, nil
	}
	if !looksLikeGalleryApp(html) {
		return nil, nil
	}
	lots, _, _, err := p.awaitItemsFeed(jc, page, pageNum)
	return lots, err
}

func mergeHits(query string, cfg pluginConfig, local []searchHit, live []parsedLot, preferred map[string]landingAuction) []searchHit {
	byURL := map[string]searchHit{}
	add := func(h searchHit) {
		if h.URL == "" || h.Score <= 0 {
			return
		}
		if loc, ok := preferred[h.AuctionID]; ok {
			h.Preferred = true
			if h.AffiliateID == "" {
				h.AffiliateID = loc.Affiliate
			}
			if h.AffiliateName == "" {
				h.AffiliateName = loc.Name
			}
			if h.AuctionTitle == "" {
				h.AuctionTitle = loc.Title
			}
		}
		if cfg.SearchScope == scopeOnly && !h.Preferred {
			return
		}
		if cfg.SearchScope == scopeAll {
			h.Preferred = false
		}
		prev, ok := byURL[h.URL]
		if !ok || h.Score > prev.Score || (h.Source == "local" && prev.Source != "local") {
			byURL[h.URL] = h
		}
	}
	for _, h := range local {
		add(h)
	}
	for _, lot := range live {
		score, reason := matchScore(query, lot.Title, "", "", "", "")
		if score <= 0 {
			score, reason = 0.2, "BidRL keyword hit"
		}
		id := lotIDFromPath(lot.URL)
		if id == "" {
			continue
		}
		add(searchHit{
			LotID: id, AuctionID: lot.AuctionID, URL: lot.URL, Title: lot.Title,
			AuctionTitle: lot.AuctionTitle, LotCode: lot.LotCode, BidCents: lot.BidCents,
			Score: score, Reason: reason, Source: "live",
		})
	}
	out := make([]searchHit, 0, len(byURL))
	for _, h := range byURL {
		out = append(out, h)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Preferred != out[j].Preferred {
			return out[i].Preferred
		}
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Title < out[j].Title
	})
	if len(out) > maxSearchHits {
		out = out[:maxSearchHits]
	}
	return out
}

func (p *Plugin) markSearch(ctx context.Context, h host.Host, id, query, scope, status string, hits int, lastErr, now string) error {
	_, err := h.Store().Exec(ctx, `INSERT INTO bidrl_searches(id, query, scope, status, hit_count, last_error, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET query = excluded.query, scope = excluded.scope, status = excluded.status,
			hit_count = excluded.hit_count, last_error = excluded.last_error`,
		id, query, scope, status, hits, lastErr, now)
	return err
}

func (p *Plugin) storeHits(ctx context.Context, h host.Host, id string, hits []searchHit) error {
	err := h.Store().Tx(ctx, func(tx hoststorage.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE bidrl_searches SET status = 'ready', hit_count = ?, last_error = '' WHERE id = ?`,
			len(hits), id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM bidrl_search_hits WHERE search_id = ?`, id); err != nil {
			return err
		}
		for i, hit := range hits {
			pref := 0
			if hit.Preferred {
				pref = 1
			}
			if _, err := tx.Exec(ctx, `INSERT INTO bidrl_search_hits(search_id, ordinal, lot_id, auction_id, url, title, auction_title, lot_code, affiliate_id, affiliate_name, preferred, bid_cents, match_score, match_reason, source)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				id, i+1, hit.LotID, hit.AuctionID, hit.URL, hit.Title, hit.AuctionTitle, hit.LotCode,
				hit.AffiliateID, hit.AffiliateName, pref, hit.BidCents, hit.Score, hit.Reason, hit.Source); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return p.pruneSearches(ctx, h)
}

func (p *Plugin) pruneSearches(ctx context.Context, h host.Host) error {
	rows, err := h.Store().Query(ctx, `SELECT id FROM bidrl_searches ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(ids) <= keepSearches {
		return nil
	}
	drop := ids[keepSearches:]
	for _, id := range drop {
		if _, err := h.Store().Exec(ctx, `DELETE FROM bidrl_search_hits WHERE search_id = ?`, id); err != nil {
			return err
		}
		if _, err := h.Store().Exec(ctx, `DELETE FROM bidrl_searches WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

func newSearchID(now time.Time) string {
	return strconv.FormatInt(now.UTC().UnixMilli(), 10)
}
