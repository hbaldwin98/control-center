package bidrl

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// sitesHomeURL is BidRL's location menu. The Turlock affiliate page
// (https://www.bidrl.com/affiliate/turlock-19/) lists the same SITES dealers.
const sitesHomeURL = "https://www.bidrl.com/api/affiliatesforhomepage"

const (
	scopePrefer = "prefer"
	scopeOnly   = "only"
	scopeAll    = "all"
)

type pluginConfig struct {
	// PreferredAffiliateIDs are numeric BidRL affiliate ids (the suffix of the
	// landing-page slug: turlock-19 → "19"). Empty means every location on
	// BidRL's SITES menu.
	PreferredAffiliateIDs []string         `json:"preferredAffiliateIds"`
	SearchScope           string           `json:"searchScope"`
	Automation            automationConfig `json:"automation"`
}

func (p *Plugin) cfg() pluginConfig {
	var c pluginConfig
	if h, ok := p.host(); ok {
		_ = h.Config().Decode(&c)
	}
	switch c.SearchScope {
	case scopePrefer, scopeOnly, scopeAll:
	default:
		c.SearchScope = scopePrefer
	}
	out := make([]string, 0, len(c.PreferredAffiliateIDs))
	seen := map[string]struct{}{}
	for _, id := range c.PreferredAffiliateIDs {
		id = affiliateIDFromSlug(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	c.PreferredAffiliateIDs = out
	c.Automation = c.Automation.normalized()
	return c
}

func (c pluginConfig) allowAffiliate(id string) bool {
	id = affiliateIDFromSlug(id)
	if id == "" {
		return false
	}
	if len(c.PreferredAffiliateIDs) == 0 {
		return true
	}
	for _, want := range c.PreferredAffiliateIDs {
		if want == id {
			return true
		}
	}
	return false
}

type homeAffiliate struct {
	Name string `json:"company_name"`
	Slug string `json:"landing_page_slug"`
	Hide string `json:"do_not_display_tab"`
}

type landingAuction struct {
	ID        string
	URL       string
	Title     string
	Affiliate string
	Name      string
	City      string
	ItemCount int
	EndsAt    string
}

type landingPage struct {
	Affiliate homeAffiliate
	Auctions  []landingAuction
}

func parseLandingPage(slug string, body []byte) (landingPage, bool) {
	var wrap struct {
		Result    string          `json:"result"`
		Affiliate json.RawMessage `json:"affiliate"`
		Auctions  json.RawMessage `json:"auctions"`
	}
	if json.Unmarshal(body, &wrap) != nil {
		return landingPage{}, false
	}
	aff := homeAffiliate{Slug: slug}
	var affObj struct {
		ID   string `json:"affiliate_id"`
		Name string `json:"aff_company_name"`
		City string `json:"aff_city"`
		Slug string `json:"landing_page_slug"`
	}
	if json.Unmarshal(wrap.Affiliate, &affObj) == nil {
		if s := sanitizeAffiliateSlug(affObj.Slug); s != "" {
			aff.Slug = s
		}
		if affObj.Name != "" {
			aff.Name = strings.TrimSpace(affObj.Name)
		}
		if aff.Name == "" {
			aff.Name = strings.TrimSpace(affObj.City)
		}
	}
	if aff.Name == "" {
		aff.Name = slug
	}
	type rawAuction struct {
		ID        string      `json:"id"`
		Title     string      `json:"title"`
		Slug      string      `json:"auction_id_slug"`
		City      string      `json:"city"`
		ItemCount json.Number `json:"item_count"`
		Ends      string      `json:"ends"`
		LastClose string      `json:"last_item_closes"`
		GroupType json.Number `json:"auction_group_type"`
	}
	var listed []rawAuction
	if len(wrap.Auctions) > 0 && wrap.Auctions[0] == '[' {
		_ = json.Unmarshal(wrap.Auctions, &listed)
	} else if len(wrap.Auctions) > 0 && wrap.Auctions[0] == '{' {
		var m map[string]rawAuction
		if json.Unmarshal(wrap.Auctions, &m) == nil {
			for _, a := range m {
				listed = append(listed, a)
			}
		}
	}
	out := landingPage{Affiliate: aff}
	affID := affiliateIDFromSlug(aff.Slug)
	for _, a := range listed {
		id := strings.TrimSpace(a.ID)
		if id == "" {
			id = auctionIDFromPath("/auction/" + a.Slug)
		}
		if id == "" {
			continue
		}
		if atoiNumber(a.GroupType) == 8 {
			continue
		}
		title := truncateRunes(collapseText(a.Title), 200)
		if title == "" {
			continue
		}
		pageURL := "https://www.bidrl.com/auction/" + id + "/bidgallery"
		if s := safePathSlug(a.Slug); s != "" && strings.Contains(s, id) {
			pageURL = "https://www.bidrl.com/auction/" + s + "/bidgallery"
		}
		ends := strings.TrimSpace(a.LastClose)
		if ends == "" {
			ends = strings.TrimSpace(a.Ends)
		}
		out.Auctions = append(out.Auctions, landingAuction{
			ID: id, URL: pageURL, Title: title, Affiliate: affID, Name: aff.Name,
			City: strings.TrimSpace(a.City), ItemCount: atoiNumber(a.ItemCount), EndsAt: ends,
		})
	}
	return out, wrap.Result == "success" || len(out.Auctions) > 0 || affID != ""
}

func parseHomeAffiliates(body []byte) []homeAffiliate {
	var raw []homeAffiliate
	if json.Unmarshal(body, &raw) != nil {
		return nil
	}
	var out []homeAffiliate
	for _, a := range raw {
		slug := sanitizeAffiliateSlug(a.Slug)
		if slug == "" || a.Hide == "1" {
			continue
		}
		name := strings.TrimSpace(a.Name)
		if name == "" {
			name = slug
		}
		out = append(out, homeAffiliate{Name: name, Slug: slug})
	}
	return out
}

func sanitizeAffiliateSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return ""
		}
	}
	if s[0] == '-' || s[len(s)-1] == '-' {
		return ""
	}
	if len(s) > 80 {
		return ""
	}
	return s
}

func safePathSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || len(s) > 200 {
		return ""
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i > 0 && i < len(s)-1:
		default:
			return ""
		}
	}
	return s
}

func affiliateIDFromSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	if allDigits(s) {
		return s
	}
	if i := strings.LastIndex(s, "-"); i >= 0 && allDigits(s[i+1:]) {
		return s[i+1:]
	}
	return ""
}

func landingPageURL(slug string) string {
	return "https://www.bidrl.com/api/landingPage/" + slug
}

func allitemsURL(keyword string, page, perPage int) string {
	k := strings.TrimSpace(keyword)
	if k == "" {
		return "https://www.bidrl.com/allitems/"
	}
	return "https://www.bidrl.com/allitems/keyword_" + url.PathEscape(k) +
		"/perpage_" + strconv.Itoa(perPage) + "/page_" + strconv.Itoa(page) + "/"
}
