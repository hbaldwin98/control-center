package bidrl

import (
	"fmt"
	"strings"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hostbrowser "github.com/hbaldwin98/control-center/host/browser"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
	hoststorage "github.com/hbaldwin98/control-center/host/storage"
)

const maxAffiliates = 20

type discoverArgs struct{}

func (p *Plugin) discoverJob(jc hostjobs.Context) error {
	h, ok := p.host()
	if !ok {
		return fmt.Errorf("bidrl: host is not initialized")
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
	n, err := p.discoverSites(jc, h, page)
	if err != nil {
		return err
	}
	if err := h.Events().Publish(jc, "sites.discovered", "sites", sitesDiscovered{AuctionCount: n}); err != nil {
		return err
	}
	return jc.Progress(1, fmt.Sprintf("%d SITES auctions", n))
}

func (p *Plugin) discoverSites(jc hostjobs.Context, h host.Host, page hostbrowser.Page) (int, error) {
	return p.discoverSitesFor(jc, h, page, nil)
}

// discoverSitesFor lists open auctions at the given locations, or at every location
// the plugin config allows when the set is empty. A sweep always passes its own
// explicit set: an unscoped scheduled discovery is a crawl, which is exactly what
// this plugin will not do.
func (p *Plugin) discoverSitesFor(jc hostjobs.Context, h host.Host, page hostbrowser.Page, only []string) (int, error) {
	cfg := p.cfg()
	allow := cfg.allowAffiliate
	if len(only) > 0 {
		wanted := make(map[string]struct{}, len(only))
		for _, id := range only {
			wanted[affiliateIDFromSlug(id)] = struct{}{}
		}
		allow = func(slug string) bool {
			_, ok := wanted[affiliateIDFromSlug(slug)]
			return ok
		}
	}
	_ = jc.Progress(0.05, "listing SITES locations")
	homes, err := p.fetchHomeAffiliates(jc, page)
	if err != nil {
		return 0, err
	}
	var want []homeAffiliate
	for _, a := range homes {
		if allow(a.Slug) {
			want = append(want, a)
		}
		if len(want) >= maxAffiliates {
			break
		}
	}
	if len(want) == 0 {
		_ = jc.Logf("no SITES affiliates from the location menu")
		return 0, nil
	}
	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	var stored []landingAuction
	for i, a := range want {
		if err := jc.Err(); err != nil {
			return 0, err
		}
		_ = jc.Progress(0.1+0.8*float64(i)/float64(len(want)), a.Name)
		listed, err := p.fetchLanding(jc, page, a)
		if err != nil {
			_ = jc.Logf("affiliate %s: %v", a.Slug, err)
			continue
		}
		stored = append(stored, listed...)
	}
	if err := p.replaceAffiliateAuctions(jc, h, stored, now, only); err != nil {
		return 0, err
	}
	_ = jc.Logf("stored %d open auctions across %d SITES locations", len(stored), len(want))
	return len(stored), nil
}

func (p *Plugin) fetchHomeAffiliates(jc hostjobs.Context, page hostbrowser.Page) ([]homeAffiliate, error) {
	res, err := page.Get(jc, sitesHomeURL)
	if err != nil {
		return nil, fmt.Errorf("bidrl: SITES location menu: %w", err)
	}
	if res.Status >= 400 {
		return nil, fmt.Errorf("bidrl: SITES location menu: HTTP %d", res.Status)
	}
	list := parseHomeAffiliates(res.Body)
	if len(list) == 0 {
		return nil, fmt.Errorf("bidrl: SITES location menu returned no affiliates")
	}
	return list, nil
}

func (p *Plugin) fetchLanding(jc hostjobs.Context, page hostbrowser.Page, aff homeAffiliate) ([]landingAuction, error) {
	res, err := page.Get(jc, landingPageURL(aff.Slug))
	if err != nil {
		return nil, err
	}
	if res.Status >= 400 {
		return nil, fmt.Errorf("HTTP %d", res.Status)
	}
	parsed, ok := parseLandingPage(aff.Slug, res.Body)
	if !ok {
		return nil, fmt.Errorf("unreadable landing page")
	}
	if parsed.Affiliate.Name == "" {
		parsed.Affiliate.Name = aff.Name
	}
	for i := range parsed.Auctions {
		if parsed.Auctions[i].Name == "" {
			parsed.Auctions[i].Name = aff.Name
		}
		if parsed.Auctions[i].Affiliate == "" {
			parsed.Auctions[i].Affiliate = affiliateIDFromSlug(aff.Slug)
		}
	}
	return parsed.Auctions, nil
}

// replaceAffiliateAuctions rewrites the discovery cache. A scoped run replaces only
// the locations it actually visited: clearing the whole table would delete listings
// for locations this run never looked at, which then look closed.
func (p *Plugin) replaceAffiliateAuctions(jc hostjobs.Context, h host.Host, listed []landingAuction, now string, only []string) error {
	return h.Store().Tx(jc, func(tx hoststorage.Tx) error {
		if len(only) == 0 {
			if _, err := tx.Exec(jc, `DELETE FROM bidrl_affiliate_auctions`); err != nil {
				return err
			}
		} else if _, err := tx.Exec(jc, `DELETE FROM bidrl_affiliate_auctions WHERE `+
			inClause("affiliate_id", len(only)), anyStrings(cleanAffiliateIDs(only))...); err != nil {
			return err
		}
		seen := map[string]struct{}{}
		for _, a := range listed {
			if _, dup := seen[a.ID]; dup {
				continue
			}
			seen[a.ID] = struct{}{}
			if _, err := tx.Exec(jc, `INSERT INTO bidrl_affiliate_auctions(id, url, title, affiliate_id, affiliate_name, city, item_count, ends_at, seen_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				a.ID, a.URL, a.Title, a.Affiliate, a.Name, a.City, a.ItemCount, a.EndsAt, now); err != nil {
				return err
			}
			// Backfill any auction we already collected. This is how an auction
			// collected from a pasted URL — never seen on a landing page at collect
			// time — learns where it is, and how a location lost before migration 7
			// comes back.
			if err := stampAuctionLocation(jc, tx, a.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

func (p *Plugin) preferredAuctionIDs(jc hostjobs.Context, h host.Host) (map[string]landingAuction, error) {
	rows, err := h.Store().Query(jc, `SELECT id, url, title, affiliate_id, affiliate_name, city, item_count, ends_at FROM bidrl_affiliate_auctions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]landingAuction{}
	for rows.Next() {
		var a landingAuction
		if err := rows.Scan(&a.ID, &a.URL, &a.Title, &a.Affiliate, &a.Name, &a.City, &a.ItemCount, &a.EndsAt); err != nil {
			return nil, err
		}
		out[a.ID] = a
	}
	return out, rows.Err()
}

func auctionURL(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	return "https://www.bidrl.com/auction/" + id + "/bidgallery"
}
