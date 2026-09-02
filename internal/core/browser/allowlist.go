package browser

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
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

// denial is an ErrDenied carrying the stable reason token plus the target that was
// rejected, so logs, events, and the error a plugin sees all name the offending URL
// instead of a bare "host".
type denial struct {
	reason string
	target string
	// reported is set once the denial has been logged and published, so an error
	// that bubbles up from the engine is not audited a second time on the way out.
	reported bool
}

func (d *denial) Error() string {
	if d.target == "" {
		return ErrDenied.Error() + ": " + d.reason
	}
	return ErrDenied.Error() + ": " + d.reason + " (" + d.target + ")"
}

func (d *denial) Unwrap() error { return ErrDenied }

// deny rejects u for reason. The URL is redacted so userinfo never reaches a log.
func deny(reason string, u *url.URL) error {
	if u == nil {
		return &denial{reason: reason}
	}
	return &denial{reason: reason, target: u.Redacted()}
}

// denyTarget rejects an already-stringified target (a raw URL, host, or host:port).
func denyTarget(reason, target string) error {
	return &denial{reason: reason, target: target}
}

func parsePageURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, denyTarget("scheme", raw)
	}
	return u, nil
}

func checkURL(u *url.URL, al allowlist) error {
	return checkURLScheme(u, al, "https")
}

// checkURLScheme is checkURL with the transport pinned by the caller. Everything past
// the scheme -- userinfo, literal IPs, the port, the allowlist -- is identical, so a
// wss:// subscription is gated exactly the way an https:// fetch is.
func checkURLScheme(u *url.URL, al allowlist, scheme string) error {
	if u == nil || u.Scheme == "" || u.Host == "" {
		return deny("scheme", u)
	}
	if !strings.EqualFold(u.Scheme, scheme) {
		return deny("scheme", u)
	}
	if u.User != nil {
		return deny("userinfo", u)
	}
	host := u.Hostname()
	if host == "" {
		return deny("host", u)
	}
	if strings.Contains(u.Host, "[") || net.ParseIP(host) != nil {
		return deny("address", u)
	}
	port := u.Port()
	if port != "" && port != "443" {
		return deny("port", u)
	}
	canon, err := canonicalHost(host)
	if err != nil {
		return deny("host", u)
	}
	if _, ok := al.hosts[canon]; !ok {
		return denyTarget("host", host+" is not in this session's allowlist: "+al.describe())
	}
	return nil
}

// describe lists the allowlisted hosts so a denial says what the session could have
// reached, which is the fact a plugin author needs to fix the list.
func (a allowlist) describe() string {
	if len(a.hosts) == 0 {
		return "(empty)"
	}
	hosts := make([]string, 0, len(a.hosts))
	for h := range a.hosts {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	return strings.Join(hosts, ", ")
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
	var d *denial
	if errors.As(err, &d) {
		return d.reason
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
		return denyTarget("address", "no address")
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || cgnat(ip) {
		return denyTarget("address", ip.String()+" is loopback, private, link-local, or CGNAT")
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
