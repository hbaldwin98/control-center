package search

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

func normalizeAllowlist(hosts []string) (map[string]struct{}, error) {
	if len(hosts) == 0 {
		return nil, nil
	}
	out := map[string]struct{}{}
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" || strings.ContainsAny(h, "/: ") || net.ParseIP(h) != nil {
			return nil, fmt.Errorf("%w: allowlist host %q", ErrInvalid, h)
		}
		out[h] = struct{}{}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: empty allowlist", ErrInvalid)
	}
	return out, nil
}

func publicHit(h Hit, allow map[string]struct{}) (Hit, error) {
	raw := strings.TrimSpace(h.URL)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return Hit{}, fmt.Errorf("%w: url", ErrDenied)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || net.ParseIP(host) != nil || strings.Contains(host, ":") {
		return Hit{}, fmt.Errorf("%w: host", ErrDenied)
	}
	port := u.Port()
	if port != "" && port != "443" {
		return Hit{}, fmt.Errorf("%w: port", ErrDenied)
	}
	if !hostAllowed(host, allow) {
		return Hit{}, fmt.Errorf("%w: allowlist", ErrDenied)
	}
	u.Host = host
	if port == "443" {
		u.Host = host
	}
	u.Fragment = ""
	title := strings.TrimSpace(h.Title)
	if len(title) > 200 {
		title = title[:200]
	}
	snip := collapseSpace(h.Snippet)
	if len(snip) > 500 {
		snip = snip[:500]
	}
	return Hit{URL: u.String(), Title: title, Snippet: snip}, nil
}

func hostAllowed(host string, allow map[string]struct{}) bool {
	if allow == nil {
		return true
	}
	if _, ok := allow[host]; ok {
		return true
	}
	for listed := range allow {
		if host == listed || strings.HasSuffix(host, "."+listed) {
			return true
		}
	}
	return false
}

func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
