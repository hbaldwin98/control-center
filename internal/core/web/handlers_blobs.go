package web

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/hbaldwin98/control-center/internal/core/storage"
)

// handleBlob serves one stored object. The request is authenticated by the wrapper; this
// handler authorizes the scope before opening the file.
//
// Responses use the validated stored MIME type with nosniff, a private one-day cache
// validated by the content digest, and Content-Disposition: attachment by default. Only
// an explicit safe-image allowlist may be served inline; SVG, HTML, and other active
// content remain attachments.
func (s *Server) handleBlob(w http.ResponseWriter, r *http.Request) {
	if s.deps.Blobs == nil {
		writeError(w, http.StatusServiceUnavailable, CodeInternal, "blob storage unavailable")
		return
	}
	scope := r.PathValue("scope")
	key := r.PathValue("key")
	if scope == "" || key == "" {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such blob")
		return
	}
	if !s.blobScopeAllowed(r, scope) {
		writeError(w, http.StatusForbidden, CodeForbidden, "not authorized for this blob scope")
		return
	}

	rc, meta, err := s.deps.Blobs.Scoped(scope).Get(r.Context(), key)
	switch {
	case err == nil:
	case errors.Is(err, storage.ErrNotFound), errors.Is(err, storage.ErrInvalidKey):
		writeError(w, http.StatusNotFound, CodeNotFound, "no such blob")
		return
	default:
		s.fail(w, "read blob", err)
		return
	}
	defer rc.Close()

	disposition := "attachment"
	if _, inline := storage.InlineImageMIMEs[meta.MIME]; inline {
		disposition = "inline"
	}

	h := w.Header()
	h.Set("Content-Type", meta.MIME)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Disposition", disposition)
	h.Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	// Blob keys are stable for a stored photo and replacements are explicit writes.
	// Keep them in the administrator's private browser cache, then use the digest
	// to validate cheaply if a key is replaced after the cache expires.
	h.Set("Cache-Control", "private, max-age=86400")
	h.Set("ETag", `"`+meta.SHA256+`"`)
	// The digest lets a client verify integrity without a second round trip.
	h.Set("X-Content-SHA256", meta.SHA256)

	if etagMatches(r.Header.Get("If-None-Match"), `"`+meta.SHA256+`"`) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	if _, err := io.Copy(w, rc); err != nil {
		// The client went away mid-transfer; nothing useful to report to it.
		return
	}
}

func etagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}

// blobScopeAllowed decides whether the authenticated administrator may read this scope.
// There is one administrator principal, so every core and registered plugin scope is
// readable; an unregistered scope is not, so a stray key cannot enumerate the filesystem.
func (s *Server) blobScopeAllowed(r *http.Request, scope string) bool {
	if sessionFrom(r.Context()) == nil {
		return false
	}
	for _, p := range s.pluginDescriptors(r.Context()) {
		if p.ID == scope {
			return true
		}
	}
	switch scope {
	case "core", "core.jobs", "core.ai", "core.browser", "core.notifications":
		return true
	}
	return false
}
