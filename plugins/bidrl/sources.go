package bidrl

import (
	"net/url"
	"sort"
	"strings"

	hostsearch "github.com/hbaldwin98/control-center/host/search"
)

// priceTiers are the preference order after a single search: eBay sold comps,
// then retail stickers, then other resale, then the open web.
type priceTier struct {
	class string
	label string
}

var priceTiers = []priceTier{
	{class: "ebay", label: "eBay"},
	{class: "retail", label: "retail"},
	{class: "marketplace", label: "marketplace"},
	{class: "web", label: "web"},
}

func tierFromClass(class string) priceTier {
	for _, t := range priceTiers {
		if t.class == class {
			return t
		}
	}
	return priceTier{class: "web", label: "web"}
}

func classRank(class string) int {
	for i, t := range priceTiers {
		if t.class == class {
			return i
		}
	}
	return len(priceTiers)
}

func rankUsableHits(hits []hostsearch.Hit, model string) []hostsearch.Hit {
	usable := usableHits(hits, model)
	if len(usable) == 0 {
		return nil
	}
	sort.SliceStable(usable, func(i, j int) bool {
		return classRank(classifySource(usable[i].URL).Class) < classRank(classifySource(usable[j].URL).Class)
	})
	best := classRank(classifySource(usable[0].URL).Class)
	var out []hostsearch.Hit
	for _, hit := range usable {
		if classRank(classifySource(hit.URL).Class) != best {
			break
		}
		out = append(out, hit)
	}
	return out
}

func rankModelHits(hits []hostsearch.Hit, model string) []hostsearch.Hit {
	usable := modelHits(hits, model)
	sort.SliceStable(usable, func(i, j int) bool {
		return classRank(classifySource(usable[i].URL).Class) < classRank(classifySource(usable[j].URL).Class)
	})
	return usable
}

func modelHits(hits []hostsearch.Hit, model string) []hostsearch.Hit {
	var out []hostsearch.Hit
	seen := map[string]bool{}
	for _, hit := range hits {
		key := strings.TrimRight(strings.ToLower(strings.TrimSpace(hit.URL)), "/")
		if key == "" || seen[key] || (!modelMatch(hit.Title, model) && !modelMatch(hit.Snippet, model)) {
			continue
		}
		seen[key] = true
		out = append(out, hit)
	}
	return out
}

type sourceInfo struct {
	Class string
	Label string
}

func classifySource(rawURL string) sourceInfo {
	u, err := url.Parse(rawURL)
	if err != nil {
		return sourceInfo{Class: "web", Label: "web"}
	}
	host := strings.ToLower(u.Hostname())
	host = strings.TrimPrefix(host, "www.")
	if host == "" {
		return sourceInfo{Class: "web", Label: "web"}
	}
	for _, row := range sourceRoots {
		if host == row.root || strings.HasSuffix(host, "."+row.root) {
			return sourceInfo{Class: row.class, Label: row.label}
		}
	}
	return sourceInfo{Class: "web", Label: host}
}

var sourceRoots = []struct {
	root  string
	class string
	label string
}{
	{"ebay.com", "ebay", "eBay"},
	{"ebay.ca", "ebay", "eBay"},
	{"ebay.co.uk", "ebay", "eBay"},
	{"mercari.com", "marketplace", "Mercari"},
	{"offerup.com", "marketplace", "OfferUp"},
	{"craigslist.org", "marketplace", "Craigslist"},
	{"facebook.com", "marketplace", "Facebook"},
	{"poshmark.com", "marketplace", "Poshmark"},
	{"shopgoodwill.com", "marketplace", "Shopgoodwill"},
	{"amazon.com", "retail", "Amazon"},
	{"amazon.ca", "retail", "Amazon"},
	{"walmart.com", "retail", "Walmart"},
	{"target.com", "retail", "Target"},
	{"homedepot.com", "retail", "Home Depot"},
	{"lowes.com", "retail", "Lowe's"},
	{"bestbuy.com", "retail", "Best Buy"},
	{"costco.com", "retail", "Costco"},
}

func usableHits(hits []hostsearch.Hit, model string) []hostsearch.Hit {
	var out []hostsearch.Hit
	for _, hit := range hits {
		if !modelMatch(hit.Title, model) && !modelMatch(hit.Snippet, model) {
			continue
		}
		if dollar.FindString(hit.Title) == "" && dollar.FindString(hit.Snippet) == "" {
			continue
		}
		out = append(out, hit)
	}
	return out
}

func hitStatesCents(hit hostsearch.Hit, cents int64) bool {
	return textStatesCents(hit.Title, cents) || textStatesCents(hit.Snippet, cents)
}

func textStatesCents(s string, cents int64) bool {
	for _, m := range dollar.FindAllStringSubmatch(s, -1) {
		if len(m) < 2 {
			continue
		}
		got, ok := parseDollars(m[1])
		if ok && got == cents {
			return true
		}
	}
	return false
}

func quoteHit(hit hostsearch.Hit, cents int64) string {
	if textStatesCents(hit.Snippet, cents) && strings.TrimSpace(hit.Snippet) != "" {
		return strings.TrimSpace(hit.Snippet)
	}
	if textStatesCents(hit.Title, cents) && strings.TrimSpace(hit.Title) != "" {
		return strings.TrimSpace(hit.Title)
	}
	if s := strings.TrimSpace(hit.Snippet); s != "" {
		return s
	}
	return strings.TrimSpace(hit.Title)
}
