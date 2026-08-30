package events

import (
	"fmt"
	"strings"
)

// An event type is a source namespace followed by one or more dot-separated segments.
// Each segment uses the ASCII grammar [a-z][a-z0-9_]*, so a complete type matches
// ^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$.
//
// The first segment is "core" for core modules or the plugin ID for plugin events.
func validateType(t string) error {
	segs := strings.Split(t, ".")
	if len(segs) < 2 {
		return fmt.Errorf("%w: %q needs at least two segments", ErrInvalidType, t)
	}
	for _, s := range segs {
		if !validSegment(s) {
			return fmt.Errorf("%w: segment %q in %q", ErrInvalidType, s, t)
		}
	}
	return nil
}

// validateSource accepts a core module source ("core.<module>") or a bare plugin ID, and
// requires the event type to sit under that namespace.
func validateSource(source, eventType string) error {
	if source == "" {
		return fmt.Errorf("%w: empty", ErrInvalidSource)
	}
	segs := strings.Split(source, ".")
	for _, s := range segs {
		if !validSegment(s) {
			return fmt.Errorf("%w: %q", ErrInvalidSource, source)
		}
	}

	var namespace string
	switch {
	case segs[0] == "core":
		if len(segs) != 2 {
			return fmt.Errorf("%w: core source %q must be core.<module>", ErrInvalidSource, source)
		}
		namespace = "core"
	default:
		if len(segs) != 1 {
			return fmt.Errorf("%w: plugin source %q must be a bare plugin ID", ErrInvalidSource, source)
		}
		namespace = segs[0]
	}

	if first, _, _ := strings.Cut(eventType, "."); first != namespace {
		return fmt.Errorf("%w: source %q cannot publish type %q", ErrInvalidSource, source, eventType)
	}
	return nil
}

func validSegment(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' {
			continue
		}
		return false
	}
	return true
}

// Pattern is a compiled subscription pattern. It uses the same literal segments as an
// event type plus two whole-segment wildcards: "*" matches exactly one segment and "**"
// matches zero or more.
type Pattern struct {
	raw  string
	segs []string
}

// String returns the pattern as written.
func (p Pattern) String() string { return p.raw }

// CompilePattern validates a pattern and prepares it for matching.
func CompilePattern(pattern string) (Pattern, error) {
	if pattern == "" {
		return Pattern{}, fmt.Errorf("%w: empty", ErrInvalidPattern)
	}
	segs := strings.Split(pattern, ".")
	for _, s := range segs {
		if s == "*" || s == "**" {
			continue
		}
		if !validSegment(s) {
			return Pattern{}, fmt.Errorf("%w: segment %q in %q", ErrInvalidPattern, s, pattern)
		}
	}
	return Pattern{raw: pattern, segs: segs}, nil
}

// MustCompilePattern is CompilePattern for package-level constants.
func MustCompilePattern(pattern string) Pattern {
	p, err := CompilePattern(pattern)
	if err != nil {
		panic(err)
	}
	return p
}

// Matches reports whether an event type matches the pattern.
func (p Pattern) Matches(eventType string) bool {
	return matchSegments(p.segs, strings.Split(eventType, "."))
}

// matchSegments walks pattern and type segments together, recursing only where "**" makes
// the split ambiguous.
func matchSegments(pat, typ []string) bool {
	for len(pat) > 0 {
		switch pat[0] {
		case "**":
			// "**" matches zero or more segments: try every split.
			rest := pat[1:]
			if len(rest) == 0 {
				return true
			}
			for i := 0; i <= len(typ); i++ {
				if matchSegments(rest, typ[i:]) {
					return true
				}
			}
			return false
		case "*":
			if len(typ) == 0 {
				return false
			}
			pat, typ = pat[1:], typ[1:]
		default:
			if len(typ) == 0 || typ[0] != pat[0] {
				return false
			}
			pat, typ = pat[1:], typ[1:]
		}
	}
	return len(typ) == 0
}

// sqlPrefix returns a LIKE prefix that is a necessary condition for a match, so the
// database can skip obviously irrelevant rows before the exact matcher runs. It returns
// an empty string when no useful prefix exists.
func (p Pattern) sqlPrefix() string {
	var b strings.Builder
	for _, s := range p.segs {
		if s == "*" || s == "**" {
			break
		}
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(s)
	}
	if b.Len() == 0 {
		return ""
	}
	// A literal prefix must be followed by "." or end the type.
	return b.String()
}
