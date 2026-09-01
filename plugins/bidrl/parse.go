package bidrl

import (
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var (
	lotHref  = regexp.MustCompile(`(?i)href=["']([^"'#]*?/auction/[^"'#]+/item/[^"'#]+)["']`)
	imgSrc   = regexp.MustCompile(`(?i)<img\b[^>]*\bsrc=["']([^"']+)["']`)
	h1Text   = regexp.MustCompile(`(?is)<h1\b[^>]*>(.*?)</h1>`)
	titleTag = regexp.MustCompile(`(?is)<title\b[^>]*>(.*?)</title>`)
	tags     = regexp.MustCompile(`(?s)<[^>]*>`)
	dollar   = regexp.MustCompile(`\$([0-9]{1,3}(?:,[0-9]{3})*(?:\.[0-9]{2})?|[0-9]+(?:\.[0-9]{2})?)`)
	lotCode  = regexp.MustCompile(`(?i)\b([A-Z]{1,3}\d{3,6})\b`)
)

type parsedLot struct {
	URL      string
	Title    string
	LotCode  string
	BidCents *int64
	// Images is set only when the source already carried full-size photographs,
	// as the JSON item feed does; an HTML-scraped lot leaves it empty and the
	// collector opens the lot page instead.
	Images []string
}

type parsedPage struct {
	Title    string
	Lots     []parsedLot
	Images   []string
	BidCents *int64
}

func parseAuctionHTML(pageURL string, raw string) parsedPage {
	base, _ := url.Parse(pageURL)
	out := parsedPage{Title: pageTitle(raw)}
	seen := map[string]struct{}{}
	for _, m := range lotHref.FindAllStringSubmatch(raw, maxLotsPerAuction+1) {
		abs, err := resolveURL(base, html.UnescapeString(m[1]))
		if err != nil || !isAllowedHost(abs.Hostname()) {
			continue
		}
		key := abs.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		id := lotIDFromPath(abs.Path)
		if id == "" {
			continue
		}
		out.Lots = append(out.Lots, parsedLot{
			URL:     abs.String(),
			Title:   nearbyLinkText(raw, m[1]),
			LotCode: lotCode.FindString(abs.Path),
		})
	}
	out.Images = pageImages(base, raw)
	out.BidCents = firstBid(raw)
	return out
}

func parseLotHTML(pageURL string, raw string) parsedPage {
	base, _ := url.Parse(pageURL)
	out := parsedPage{
		Title:    pageTitle(raw),
		Images:   pageImages(base, raw),
		BidCents: firstBid(raw),
	}
	if code := lotCode.FindString(out.Title + " " + pageURL); code != "" {
		out.Lots = []parsedLot{{URL: pageURL, Title: out.Title, LotCode: code, BidCents: out.BidCents}}
	}
	return out
}

func pageTitle(raw string) string {
	if m := h1Text.FindStringSubmatch(raw); len(m) > 1 {
		if t := collapseText(m[1]); t != "" {
			return truncateRunes(t, 200)
		}
	}
	if m := titleTag.FindStringSubmatch(raw); len(m) > 1 {
		t := collapseText(m[1])
		if i := strings.Index(t, "|"); i > 0 {
			t = strings.TrimSpace(t[:i])
		}
		return truncateRunes(t, 200)
	}
	return ""
}

func pageImages(base *url.URL, raw string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, m := range imgSrc.FindAllStringSubmatch(raw, 64) {
		abs, err := resolveURL(base, html.UnescapeString(m[1]))
		if err != nil || !isAllowedHost(abs.Hostname()) {
			continue
		}
		if !looksLikeImage(abs.Path) && !strings.Contains(abs.Host, "cloudfront") && !strings.Contains(abs.Path, "/image") {
			// Still accept https images from the allowlist; logos are filtered by size later.
		}
		key := abs.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, abs.String())
		if len(out) >= maxImagesPerLot {
			break
		}
	}
	return out
}

func looksLikeImage(path string) bool {
	low := strings.ToLower(path)
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp", ".gif"} {
		if strings.HasSuffix(low, ext) {
			return true
		}
	}
	return false
}

func firstBid(raw string) *int64 {
	m := dollar.FindStringSubmatch(raw)
	if len(m) < 2 {
		return nil
	}
	cents, ok := parseDollars(m[1])
	if !ok {
		return nil
	}
	return &cents
}

func parseDollars(s string) (int64, bool) {
	s = strings.ReplaceAll(s, ",", "")
	if s == "" {
		return 0, false
	}
	if i := strings.IndexByte(s, '.'); i >= 0 {
		dollars, err := strconv.ParseInt(s[:i], 10, 64)
		if err != nil {
			return 0, false
		}
		frac := s[i+1:]
		if len(frac) == 1 {
			frac += "0"
		}
		if len(frac) > 2 {
			frac = frac[:2]
		}
		cents, err := strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return 0, false
		}
		return dollars*100 + cents, true
	}
	dollars, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return dollars * 100, true
}

func nearbyLinkText(raw, href string) string {
	// Prefer the text of the <a> that contains this href.
	idx := strings.Index(raw, href)
	if idx < 0 {
		return ""
	}
	rest := raw[idx:]
	gt := strings.IndexByte(rest, '>')
	if gt < 0 {
		return ""
	}
	end := strings.Index(strings.ToLower(rest[gt+1:]), "</a>")
	if end < 0 {
		return ""
	}
	return truncateRunes(collapseText(rest[gt+1:gt+1+end]), 200)
}

func collapseText(raw string) string {
	raw = tags.ReplaceAllString(raw, " ")
	raw = html.UnescapeString(raw)
	return strings.Join(strings.Fields(raw), " ")
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

func imageMIME(mime string) bool {
	switch strings.ToLower(strings.TrimSpace(strings.Split(mime, ";")[0])) {
	case "image/jpeg", "image/jpg", "image/png", "image/webp", "image/gif", "image/avif":
		return true
	default:
		return false
	}
}

func imageExt(mime string) string {
	switch strings.ToLower(strings.TrimSpace(strings.Split(mime, ";")[0])) {
	case "image/png":
		return "png"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	case "image/avif":
		return "avif"
	default:
		return "jpg"
	}
}

// looksLikeGalleryApp reports whether the document booted BIDRL's AngularJS app, which
// is what fetches the item feed. Markup without it will never produce one.
func looksLikeGalleryApp(raw string) bool {
	low := strings.ToLower(raw)
	for _, marker := range []string{"ng-app", "ng-controller", "angular.module", "/js/app.rev"} {
		if strings.Contains(low, marker) {
			return true
		}
	}
	return false
}
