package notifications

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/events"
)

func validID(s string) bool {
	if s == "" {
		return false
	}
	for _, part := range strings.Split(s, ".") {
		if !validIDSegment(part) {
			return false
		}
	}
	return true
}

func validIDSegment(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func validateURL(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if strings.ContainsAny(raw, "{}") {
		if err := validateTemplate(raw); err != nil {
			return "", err
		}
		// A whole URL may come from one event field (for example
		// {event.payload.url}), so validate the shape with a safe placeholder and
		// validate the rendered value again when the event is handled.
		probe := templateURLProbe(raw)
		if !strings.HasPrefix(probe, "/") {
			if !strings.HasPrefix(raw, "{") {
				return "", ErrInvalidURL
			}
			probe = "/" + probe
		}
		if _, err := validateStaticURL(probe); err != nil {
			return "", err
		}
		return raw, nil
	}
	return validateStaticURL(raw)
}

func validateStaticURL(raw string) (string, error) {
	if strings.Contains(raw, `\`) || strings.Contains(raw, "://") || strings.HasPrefix(raw, "//") {
		return "", ErrInvalidURL
	}
	if !strings.HasPrefix(raw, "/") {
		return "", ErrInvalidURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return "", ErrInvalidURL
	}
	if strings.Contains(u.Path, "..") {
		return "", ErrInvalidURL
	}
	return u.RequestURI(), nil
}

func templateURLProbe(raw string) string {
	var b strings.Builder
	for {
		i := strings.IndexByte(raw, '{')
		if i < 0 {
			b.WriteString(raw)
			return b.String()
		}
		b.WriteString(raw[:i])
		rest := raw[i+1:]
		j := strings.IndexByte(rest, '}')
		if j < 0 {
			// validateTemplate reports the useful error; keep this helper total.
			b.WriteString(rest)
			return b.String()
		}
		b.WriteByte('x')
		raw = rest[j+1:]
	}
}

func throttleSubject(e events.Event) (source, value string) {
	if e.Subject != "" {
		return e.Source, e.Subject
	}
	return "", e.Type
}

func eventUniq(ruleID string, eventID int64) string {
	b, _ := json.Marshal(struct {
		K string `json:"k"`
		R string `json:"r"`
		E int64  `json:"e"`
	}{K: "event", R: ruleID, E: eventID})
	return string(b)
}

func windowUniq(ruleID, subjSource, subjValue, windowStart string) string {
	b, _ := json.Marshal(struct {
		K string `json:"k"`
		R string `json:"r"`
		S string `json:"s"`
		V string `json:"v"`
		W string `json:"w"`
	}{K: "window", R: ruleID, S: subjSource, V: subjValue, W: windowStart})
	return string(b)
}

func sendUniq(notifKey, channelID string) string {
	b, _ := json.Marshal(struct {
		K string `json:"k"`
		U string `json:"u"`
		C string `json:"c"`
	}{K: "send", U: notifKey, C: channelID})
	return string(b)
}

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}

func parseTimePtr(s *string) *time.Time {
	if s == nil || *s == "" {
		return nil
	}
	t, err := parseTime(*s)
	if err != nil {
		return nil
	}
	return &t
}

func encodeChannels(ch []string) (string, error) {
	if ch == nil {
		ch = []string{}
	}
	b, err := json.Marshal(ch)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func decodeChannels(s string) []string {
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil || out == nil {
		return []string{}
	}
	return out
}

func encodeSettings(m map[string]string) (string, error) {
	if m == nil {
		m = map[string]string{}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func decodeSettings(s string) map[string]string {
	var out map[string]string
	if err := json.Unmarshal([]byte(s), &out); err != nil || out == nil {
		return map[string]string{}
	}
	return out
}

func hasInbox(channels []string) bool {
	for _, c := range channels {
		if c == "inbox" {
			return true
		}
	}
	return false
}

func skipNotificationEvent(eventType string) bool {
	return strings.HasPrefix(eventType, "core.notification.")
}

func backoff(attempt int) time.Duration {
	d := time.Second
	for range attempt - 1 {
		d *= 2
		if d >= 5*time.Minute {
			return 5 * time.Minute
		}
	}
	return d
}

func newID() string {
	var b [16]byte
	if _, err := randRead(b[:]); err != nil {
		panic(fmt.Sprintf("notifications: crypto/rand: %v", err))
	}
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 32)
	for i, v := range b {
		out[i*2] = hexdigits[v>>4]
		out[i*2+1] = hexdigits[v&0x0f]
	}
	return string(out)
}
