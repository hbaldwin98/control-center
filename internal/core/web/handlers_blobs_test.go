package web

import (
	"bytes"
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hbaldwin98/control-center/internal/core/storage"
)

func newBlobHarness(t *testing.T) (*harness, *storage.BlobStore) {
	t.Helper()
	h := newHarness(t)
	blobs, err := storage.NewBlobStore(h.store, storage.BlobOptions{
		Dir:            filepath.Join(t.TempDir(), "blobs"),
		MaxObjectBytes: 1 << 20,
		MaxScopeBytes:  4 << 20,
	})
	if err != nil {
		t.Fatalf("blobs: %v", err)
	}
	h.server.deps.Blobs = blobs
	return h, blobs
}

func TestBlobRequiresAuthentication(t *testing.T) {
	h, _ := newBlobHarness(t)
	rec := h.do(http.MethodGet, "/api/blobs/core/note.txt", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
}

func TestBlobServesAttachmentAndInlineAllowlist(t *testing.T) {
	h, blobs := newBlobHarness(t)
	h.bootstrapAdmin()
	ctx := context.Background()

	if _, err := blobs.Scoped("core").Put(ctx, "notes/readme.txt", strings.NewReader("hello"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	png := []byte{0x89, 'P', 'N', 'G'}
	if _, err := blobs.Scoped("core").Put(ctx, "img/dot.png", bytes.NewReader(png), "image/png"); err != nil {
		t.Fatal(err)
	}

	rec := h.do(http.MethodGet, "/api/blobs/core/notes/readme.txt", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("text: %d %s", rec.Code, rec.Body)
	}
	if rec.Body.String() != "hello" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "text/plain" {
		t.Fatalf("content-type = %q", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff")
	}
	if rec.Header().Get("Content-Disposition") != "attachment" {
		t.Fatalf("disposition = %q, want attachment", rec.Header().Get("Content-Disposition"))
	}
	if rec.Header().Get("X-Content-SHA256") == "" {
		t.Fatal("missing digest")
	}

	rec = h.do(http.MethodGet, "/api/blobs/core/img/dot.png", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("png: %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Content-Disposition") != "inline" {
		t.Fatalf("png disposition = %q, want inline", rec.Header().Get("Content-Disposition"))
	}

	head := h.do(http.MethodHead, "/api/blobs/core/notes/readme.txt", nil)
	if head.Code != http.StatusOK {
		t.Fatalf("head: %d", head.Code)
	}
	if head.Body.Len() != 0 {
		t.Fatalf("HEAD must not write a body, got %q", head.Body.String())
	}
}

func TestBlobRejectsUnknownScopeAndMissingKey(t *testing.T) {
	h, blobs := newBlobHarness(t)
	h.bootstrapAdmin()
	ctx := context.Background()
	if _, err := blobs.Scoped("stray").Put(ctx, "secret.bin", strings.NewReader("nope"), "application/octet-stream"); err != nil {
		t.Fatal(err)
	}

	if rec := h.do(http.MethodGet, "/api/blobs/stray/secret.bin", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("unregistered scope: got %d, want 403", rec.Code)
	}
	if rec := h.do(http.MethodGet, "/api/blobs/core/missing.txt", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing key: got %d, want 404", rec.Code)
	}
}
