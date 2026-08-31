package notifications

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/events"
)

func interpolate(tmpl string, e events.Event, payload any, collapsed int) (string, error) {
	var b strings.Builder
	rest := tmpl
	for {
		i := strings.IndexByte(rest, '{')
		if i < 0 {
			b.WriteString(rest)
			return b.String(), nil
		}
		b.WriteString(rest[:i])
		rest = rest[i+1:]
		j := strings.IndexByte(rest, '}')
		if j < 0 {
			return "", fmt.Errorf("%w: unmatched '{'", ErrInvalidTemplate)
		}
		path := rest[:j]
		rest = rest[j+1:]
		val, err := lookupPath(path, e, payload, collapsed)
		if err != nil {
			return "", err
		}
		b.WriteString(escapePlain(val))
	}
}

func validateTemplate(tmpl string) error {
	rest := tmpl
	for {
		i := strings.IndexByte(rest, '{')
		if i < 0 {
			return nil
		}
		rest = rest[i+1:]
		j := strings.IndexByte(rest, '}')
		if j < 0 {
			return fmt.Errorf("%w: unmatched '{'", ErrInvalidTemplate)
		}
		path := rest[:j]
		rest = rest[j+1:]
		if path != "collapsed" && !strings.HasPrefix(path, "event.") {
			return fmt.Errorf("%w: unknown path %q", ErrInvalidTemplate, path)
		}
		if strings.ContainsAny(path, " \t\n") {
			return fmt.Errorf("%w: whitespace in path %q", ErrInvalidTemplate, path)
		}
	}
}

func lookupPath(path string, e events.Event, payload any, collapsed int) (string, error) {
	if path == "collapsed" {
		return strconv.Itoa(collapsed), nil
	}
	switch path {
	case "event.id":
		return strconv.FormatInt(e.ID, 10), nil
	case "event.type":
		return e.Type, nil
	case "event.source":
		return e.Source, nil
	case "event.subject":
		return e.Subject, nil
	}
	const prefix = "event.payload."
	if strings.HasPrefix(path, prefix) {
		return stringify(walk(payload, strings.Split(strings.TrimPrefix(path, prefix), "."))), nil
	}
	if path == "event.payload" {
		return stringify(payload), nil
	}
	return "", fmt.Errorf("%w: unknown path %q", ErrInvalidTemplate, path)
}

func walk(v any, parts []string) any {
	cur := v
	for _, p := range parts {
		if p == "" {
			return nil
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

func stringify(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano)
	default:
		return fmt.Sprint(t)
	}
}

func escapePlain(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}
