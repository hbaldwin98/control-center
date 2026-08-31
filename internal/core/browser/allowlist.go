package browser

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/net/idna"
)

type allowlist struct {
	hosts map[string]struct{}
}

func parseAllowlist(hosts []string) (allowlist, error) {
	if len(hosts) == 0 {
		return allowlist{}, ErrInvalidAllowlist
	}
	al := allowlist{hosts: map[string]struct{}{}}
	for _, raw := range hosts {
		h, err := canonicalHost(raw)
		if err != nil {
			return allowlist{}, fmt.Errorf("%w: %v", ErrInvalidAllowlist, err)
		}
		al.hosts[h] = struct{}{}
	}
	if len(al.hosts) == 0 {
		return allowlist{}, ErrInvalidAllowlist
	}
	return al, nil
}

func canonicalHost(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, ".")
	if s == "" || strings.ContainsAny(s, "/:@[] ") || strings.Contains(s, "..") {
		return "", fmt.Errorf("not a DNS name %q", raw)
	}
	if _, port, ok := strings.Cut(s, ":"); ok {
		if port != "" {
			return "", fmt.Errorf("host must not include a port: %q", raw)
		}
	}
	if ip := net.ParseIP(s); ip != nil {
		return "", fmt.Errorf("host must not be an IP: %q", raw)
	}
	ascii, err := idna.Lookup.ToASCII(s)
	if err != nil {
		return "", fmt.Errorf("host %q: %w", raw, err)
	}
	ascii = strings.ToLower(ascii)
	if ascii == "" || net.ParseIP(ascii) != nil {
		return "", fmt.Errorf("not a DNS name %q", raw)
	}
	for _, label := range strings.Split(ascii, ".") {
		if label == "" || !dnsLabel(label) {
			return "", fmt.Errorf("not a DNS name %q", raw)
		}
	}
	return ascii, nil
}

func dnsLabel(s string) bool {
	if len(s) == 0 || len(s) > 63 {
		return false
	}
	for i, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			continue
		}
		if r == '-' && i != 0 && i != len(s)-1 {
			continue
		}
		if unicode.IsUpper(r) {
			return false
		}
		return false
	}
	return true
}

func parsePageURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: scheme", ErrDenied)
	}
	return u, nil
}

func checkURL(u *url.URL, al allowlist) error {
	if u == nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("%w: scheme", ErrDenied)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("%w: scheme", ErrDenied)
	}
	if u.User != nil {
		return fmt.Errorf("%w: userinfo", ErrDenied)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: host", ErrDenied)
	}
	if strings.Contains(u.Host, "[") || net.ParseIP(host) != nil {
		return fmt.Errorf("%w: address", ErrDenied)
	}
	port := u.Port()
	if port != "" && port != "443" {
		return fmt.Errorf("%w: port", ErrDenied)
	}
	canon, err := canonicalHost(host)
	if err != nil {
		return fmt.Errorf("%w: host", ErrDenied)
	}
	if _, ok := al.hosts[canon]; !ok {
		return fmt.Errorf("%w: host", ErrDenied)
	}
	return nil
}

func deniedHost(u *url.URL) string {
	if u == nil {
		return ""
	}
	if h := u.Hostname(); h != "" {
		return h
	}
	return u.Host
}

func deniedReason(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	for _, r := range []string{"scheme", "host", "port", "userinfo", "address"} {
		if strings.HasSuffix(msg, r) {
			return r
		}
	}
	return "denied"
}

// checkResolvedIP rejects private, loopback, link-local, and CGNAT addresses. The
// Fake engine never resolves; a production engine must call this at connect time.
func checkResolvedIP(ip net.IP) error {
	if ip == nil {
		return fmt.Errorf("%w: address", ErrDenied)
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || cgnat(ip) {
		return fmt.Errorf("%w: address", ErrDenied)
	}
	return nil
}

func cgnat(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	return ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127
}
