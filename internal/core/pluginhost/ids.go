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
