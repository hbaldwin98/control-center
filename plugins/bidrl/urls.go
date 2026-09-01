package bidrl

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"unicode"
)

// allowedHosts is the compiled HTTPS DNS allowlist passed to Browser().Open.
// Userinfo, alternate ports, IP literals, and every other origin are rejected.
var allowedHosts = []string{
	"www.bidrl.com",
	"bidrl.com",
	"d3ugkdpeq35ojy.cloudfront.net",
}

func allowedHostSet() map[string]struct{} {
	out := make(map[string]struct{}, len(allowedHosts))
	for _, h := range allowedHosts {
		out[h] = struct{}{}
	}
	return out
}

func isAllowedHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	_, ok := allowedHostSet()[host]
	return ok
}

// parseAuctionURL accepts a BIDRL auction or print-catalog URL and returns the
// canonical URL, DNS host, and stable auction id.
func parseAuctionURL(raw string) (canonical, host, id string, err error) {
	u, err := parseHTTPS(raw)
	if err != nil {
		return "", "", "", err
	}
	if !isAllowedHost(u.Hostname()) {
		return "", "", "", fmt.Errorf("bidrl: host %q is not on the compiled allowlist", u.Hostname())
	}
	id = auctionIDFromPath(u.Path)
	if id == "" {
		return "", "", "", fmt.Errorf("bidrl: could not read an auction id from %s", u.Redacted())
	}
	host = strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	return u.String(), host, id, nil
}

func parseHTTPS(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return nil, fmt.Errorf("bidrl: url must be https with a DNS host")
	}
	if u.User != nil || (u.Port() != "" && u.Port() != "443") || net.ParseIP(u.Hostname()) != nil {
		return nil, fmt.Errorf("bidrl: url cannot contain userinfo, an IP address, or an alternate port")
	}
	u.Fragment = ""
	return u, nil
}

func auctionIDFromPath(path string) string {
	parts := splitPath(path)
	for i, p := range parts {
		if p == "auction" && i+1 < len(parts) {
			return slugID(parts[i+1])
		}
	}
	return ""
}

func lotIDFromPath(path string) string {
	parts := splitPath(path)
	for i, p := range parts {
		if p == "item" && i+1 < len(parts) {
			return slugID(parts[i+1])
		}
	}
	if len(parts) > 0 {
		return slugID(parts[len(parts)-1])
	}
	return ""
}

func slugID(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if allDigits(s) {
		return s
	}
	if i := strings.LastIndex(s, "-"); i >= 0 && allDigits(s[i+1:]) {
		return s[i+1:]
	}
	return sanitizeID(s)
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func sanitizeID(s string) string {
	var b strings.Builder
	for i, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case i > 0 && (r == '-' || r == '_' || r == '.'):
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-._")
	if out == "" {
		return ""
	}
	if out[0] < '0' || (out[0] > '9' && out[0] < 'a') {
		out = "a" + out
	}
	if len(out) > 128 {
		out = out[:128]
	}
	return out
}

func splitPath(path string) []string {
	var out []string
	for _, p := range strings.Split(path, "/") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func resolveURL(base *url.URL, ref string) (*url.URL, error) {
	if base == nil {
		return parseHTTPS(ref)
	}
	r, err := base.Parse(strings.TrimSpace(ref))
	if err != nil {
		return nil, err
	}
	if r.Scheme != "https" || r.Hostname() == "" {
		return nil, fmt.Errorf("bidrl: not https")
	}
	if r.User != nil || (r.Port() != "" && r.Port() != "443") || net.ParseIP(r.Hostname()) != nil {
		return nil, fmt.Errorf("bidrl: denied")
	}
	r.Fragment = ""
	return r, nil
}

func bidgalleryURL(auctionURL string) string {
	u, err := url.Parse(auctionURL)
	if err != nil {
		return auctionURL
	}
	path := strings.TrimSuffix(u.Path, "/")
	if strings.HasSuffix(path, "/bidgallery") {
		return u.String()
	}
	if strings.Contains(path, "/print_catalog/") {
		id := auctionIDFromPath(path)
		if id == "" {
			return auctionURL
		}
		u.Path = "/auction/" + id + "/bidgallery"
		u.RawQuery = ""
		return u.String()
	}
	if strings.Contains(path, "/item/") {
		return auctionURL
	}
	u.Path = path + "/bidgallery"
	return u.String()
}

func printCatalogURL(host, auctionID string) string {
	return "https://" + host + "/print_catalog/index/auction/" + auctionID
}

func blobSafe(s string) string {
	if s == "" {
		return "x"
	}
	var b strings.Builder
	for i, r := range s {
		switch {
		case unicode.IsLetter(r) || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		case i > 0 && (r == '.' || r == '_' || r == '-'):
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	if out == "" || !(out[0] >= '0' && out[0] <= '9' || out[0] >= 'A' && out[0] <= 'Z' || out[0] >= 'a' && out[0] <= 'z') {
		out = "a" + out
	}
	if len(out) > 128 {
		return out[:128]
	}
	return out
}

// galleryPageURL is the bid gallery with BIDRL's ui-router path parameters, which the
// Angular app turns into the filters it POSTs to /api/getitems.
func galleryPageURL(auctionURL string, page, perPage int) string {
	base := strings.TrimSuffix(bidgalleryURL(auctionURL), "/")
	return fmt.Sprintf("%s/perpage_%d/page_%d/", base, perPage, page)
}
