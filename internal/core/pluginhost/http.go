package pluginhost

import (
	"context"
	"net/http"
	"strings"
)

// ShellPlugin is the bootstrap view the shell reconciles against frontend modules.
type ShellPlugin struct {
	ID      string
	Name    string
	Enabled bool
}

// ServeHTTP dispatches /api/plugins/<id>/... Host-owned 503 is returned when the
// plugin is disabled or degraded; plugin code is not invoked.
func (r *Registry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	id, rel, ok := splitPluginPath(req.URL.Path)
	if !ok {
		http.NotFound(w, req)
		return
	}
	s, found := r.lookup(id)
	if !found {
		http.NotFound(w, req)
		return
	}

	s.mu.Lock()
	mux := s.mux
	gen := s.gen
	runtime := s.health.Runtime
	s.mu.Unlock()

	if mux == nil || runtime != runtimeEnabled {
		writePluginDisabled(w)
		return
	}

	ctx := req.Context()
	if gen != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		stop := context.AfterFunc(gen, cancel)
		defer stop()
		defer cancel()
	}

	cloned := req.Clone(ctx)
	cloned.URL.Path = rel
	if cloned.URL.RawPath != "" {
		cloned.URL.RawPath = rel
	}

	defer func() {
		if rec := recover(); rec != nil {
			r.opts.Log.Error("pluginhost: plugin handler panic", "plugin", id, "panic", rec)
			http.Error(w, `{"error":{"code":"internal","message":"internal error"}}`, http.StatusInternalServerError)
		}
	}()

	if req.Body != nil {
		cloned.Body = http.MaxBytesReader(w, req.Body, 1<<20)
	}
	mux.ServeHTTP(w, cloned)
}

func splitPluginPath(path string) (id, rel string, ok bool) {
	const prefix = "/api/plugins/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(path, prefix)
	if rest == "" {
		return "", "", false
	}
	id, remainder, _ := strings.Cut(rest, "/")
	if id == "" || !validPluginID(id) {
		return "", "", false
	}
	if remainder == "" {
		rel = "/"
	} else {
		rel = "/" + remainder
	}
	return id, rel, true
}

func writePluginDisabled(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`{"error":{"code":"plugin_disabled","message":"plugin disabled"}}`))
}

// ShellPlugins is the bootstrap list the shell reconciles against.
func (r *Registry) ShellPlugins(_ context.Context) []ShellPlugin {
	list := r.List()
	out := make([]ShellPlugin, 0, len(list))
	for _, d := range list {
		out = append(out, ShellPlugin{ID: d.Manifest.ID, Name: d.Manifest.Name, Enabled: d.State.Enabled})
	}
	return out
}
