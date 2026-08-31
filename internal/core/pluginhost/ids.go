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
