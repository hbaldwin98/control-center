package web

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// serveStatic serves the built single-page shell. Unknown non-API paths fall back to
// index.html so client-side routing works on a hard reload.
//
// Hashed build assets are immutable; index.html must never be cached, or a deploy leaves
// clients on a stale bundle.
func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such endpoint")
		return
	}
	dir := s.deps.StaticDir
	if dir == "" {
		s.servePlaceholder(w)
		return
	}

	clean := path.Clean("/" + r.URL.Path)
	target := filepath.Join(dir, filepath.FromSlash(clean))

	info, err := os.Stat(target)
	if err != nil || info.IsDir() {
		s.serveIndex(w, dir)
		return
	}
	if strings.HasPrefix(clean, "/assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeFile(w, r, target)
}

func (s *Server) serveIndex(w http.ResponseWriter, dir string) {
	body, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		s.servePlaceholder(w)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(body)
}

// placeholder is served when no frontend build is present, so the API is still reachable
// and the operator sees why the page is blank.
const placeholder = `<!doctype html>
<meta charset="utf-8">
<title>Control Center</title>
<style>
  body { font: 14px ui-sans-serif, system-ui, sans-serif; margin: 4rem auto; max-width: 40rem;
         color: #1c1c1f; background: #fafaf9; }
  code { background: #eceae6; padding: .1rem .3rem; border-radius: .2rem; }
</style>
<h1>Control Center</h1>
<p>The API is running, but no frontend build was found.</p>
<p>Build the shell with <code>npm --prefix web install &amp;&amp; npm --prefix web run build</code>,
   or run <code>npm --prefix web run dev</code> for the dev server.</p>
`

func (s *Server) servePlaceholder(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(placeholder))
}
