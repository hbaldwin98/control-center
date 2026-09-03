package pluginhost

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/hbaldwin98/control-center/host"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
)

type declaration struct {
	manifest host.Manifest
	jobs     []hostjobs.Def
	subs     []host.Subscription
	routes   []host.Route
	jobNames []string
	routePat []RouteDescriptor
}

func validPluginID(id string) bool {
	if id == "" || len(id) > 63 || id == "core" {
		return false
	}
	if id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for i := 1; i < len(id); i++ {
		c := id[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' {
			continue
		}
		return false
	}
	return true
}

func validName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	if s[0] < 'a' || s[0] > 'z' {
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

// validRouteName matches the AI module's logical route names: lowercase letters,
// digits, hyphen, underscore. Job names forbid hyphen; model names use it
// (cheap-vision, grounded-price).
func validRouteName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i, c := range s {
		ok := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-'
		if i == 0 && (c < 'a' || c > 'z') {
			return false
		}
		if !ok {
			return false
		}
	}
	return true
}

var knownModelCaps = map[string]struct{}{
	"chat":      {},
	"vision":    {},
	"grounding": {},
	"embed":     {},
}

func validateModelNeeds(pluginID string, needs []host.ModelNeed) error {
	seen := map[string]struct{}{}
	for i, n := range needs {
		if !validRouteName(n.Name) {
			return fmt.Errorf("%w: %s models[%d] name %q", ErrInvalidPlugin, pluginID, i, n.Name)
		}
		if _, dup := seen[n.Name]; dup {
			return fmt.Errorf("%w: %s duplicate model %q", ErrInvalidPlugin, pluginID, n.Name)
		}
		seen[n.Name] = struct{}{}
		purpose := strings.TrimSpace(n.Purpose)
		if purpose == "" || len(purpose) > 200 {
			return fmt.Errorf("%w: %s model %q needs a purpose of 1..200 characters", ErrInvalidPlugin, pluginID, n.Name)
		}
		if len(n.Capabilities) == 0 {
			return fmt.Errorf("%w: %s model %q declares no capabilities", ErrInvalidPlugin, pluginID, n.Name)
		}
		seenCap := map[string]struct{}{}
		for _, c := range n.Capabilities {
			if _, ok := knownModelCaps[c]; !ok {
				return fmt.Errorf("%w: %s model %q capability %q", ErrInvalidPlugin, pluginID, n.Name, c)
			}
			if _, dup := seenCap[c]; dup {
				return fmt.Errorf("%w: %s model %q duplicate capability %q", ErrInvalidPlugin, pluginID, n.Name, c)
			}
			seenCap[c] = struct{}{}
		}
	}
	return nil
}

var knownEventFieldTypes = map[string]struct{}{
	"string":   {},
	"number":   {},
	"boolean":  {},
	"string[]": {},
	"object":   {},
	"object[]": {},
}

func validateEventSpecs(pluginID string, specs []host.EventSpec) error {
	seen := map[string]struct{}{}
	for i, s := range specs {
		if !validEventType(s.Type) {
			return fmt.Errorf("%w: %s events[%d] type %q", ErrInvalidPlugin, pluginID, i, s.Type)
		}
		if _, dup := seen[s.Type]; dup {
			return fmt.Errorf("%w: %s duplicate event %q", ErrInvalidPlugin, pluginID, s.Type)
		}
		seen[s.Type] = struct{}{}
		purpose := strings.TrimSpace(s.Purpose)
		if purpose == "" || len(purpose) > 200 {
			return fmt.Errorf("%w: %s event %q needs a purpose of 1..200 characters", ErrInvalidPlugin, pluginID, s.Type)
		}
		seenField := map[string]struct{}{}
		for j, f := range s.Fields {
			if !validFieldName(f.Name) {
				return fmt.Errorf("%w: %s event %q fields[%d] name %q", ErrInvalidPlugin, pluginID, s.Type, j, f.Name)
			}
			if _, dup := seenField[f.Name]; dup {
				return fmt.Errorf("%w: %s event %q duplicate field %q", ErrInvalidPlugin, pluginID, s.Type, f.Name)
			}
			seenField[f.Name] = struct{}{}
			if _, ok := knownEventFieldTypes[f.Type]; !ok {
				return fmt.Errorf("%w: %s event %q field %q type %q", ErrInvalidPlugin, pluginID, s.Type, f.Name, f.Type)
			}
			fieldPurpose := strings.TrimSpace(f.Purpose)
			if fieldPurpose == "" || len(fieldPurpose) > 200 {
				return fmt.Errorf("%w: %s event %q field %q needs a purpose of 1..200 characters", ErrInvalidPlugin, pluginID, s.Type, f.Name)
			}
		}
	}
	return nil
}

// validEventType is one or more [a-z][a-z0-9_]* segments joined by dots, unprefixed.
// "ticked" and "finding.created" are valid; wildcards and uppercase are not.
func validEventType(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, seg := range strings.Split(s, ".") {
		if !validName(seg) {
			return false
		}
	}
	return true
}

// validFieldName is a JSON object key: lowercase start, then letters or digits.
// validFieldName is a payload path: one or more segments joined by dots, so a
// nested field can be declared as the path a rule actually writes. A segment
// starts with a lowercase letter and may carry digits, letters, and underscores,
// because a payload key is whatever the plugin's JSON tag says it is.
func validFieldName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, seg := range strings.Split(s, ".") {
		if !validFieldSegment(seg) {
			return false
		}
	}
	return true
}

func validFieldSegment(s string) bool {
	if s == "" {
		return false
	}
	if s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
			continue
		}
		return false
	}
	return true
}

func parseRoutePattern(pattern string) (method, path string, err error) {
	pattern = strings.TrimSpace(pattern)
	method, path, ok := strings.Cut(pattern, " ")
	if !ok {
		return "", "", fmt.Errorf("route %q needs a method and a path", pattern)
	}
	method = strings.ToUpper(method)
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions:
	default:
		return "", "", fmt.Errorf("route %q has an unsupported method", pattern)
	}
	if !strings.HasPrefix(path, "/") {
		return "", "", fmt.Errorf("route %q path must be relative and start with /", pattern)
	}
	if strings.Contains(path, "..") || strings.Contains(path, "//") {
		return "", "", fmt.Errorf("route %q path is not canonical", pattern)
	}
	if strings.HasPrefix(path, "/api/") {
		return "", "", fmt.Errorf("route %q escapes the plugin mount", pattern)
	}
	return method, path, nil
}

func durableName(pluginID, local string) string {
	return "plugin:" + pluginID + ":" + local
}
