package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newBlobs(t *testing.T, maxObject, maxScope int64) (*BlobStore, Blobs) {
	t.Helper()
	s := openTest(t)
	bs, err := NewBlobStore(s, BlobOptions{
		Dir:            filepath.Join(t.TempDir(), "blobs"),
		MaxObjectBytes: maxObject,
		MaxScopeBytes:  maxScope,
	})
	if err != nil {
		t.Fatalf("NewBlobStore: %v", err)
	}
	return bs, bs.Scoped("demo")
}

func TestBlobPutGetDelete(t *testing.T) {
	ctx := context.Background()
	_, b := newBlobs(t, 1<<20, 4<<20)

	ref, err := b.Put(ctx, "reports/2026/summary.json", strings.NewReader(`{"ok":true}`), "application/json")
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if ref.Size != 11 {
		t.Fatalf("size = %d, want 11", ref.Size)
	}
	if ref.SHA256 == "" {
		t.Fatal("missing digest")
	}

	rc, meta, err := b.Get(ctx, "reports/2026/summary.json")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	body, _ := io.ReadAll(rc)
	rc.Close()
	if string(body) != `{"ok":true}` {
		t.Fatalf("body = %q", body)
	}
	if meta.MIME != "application/json" {
		t.Fatalf("mime = %q", meta.MIME)
	}
	if meta.CreatedAt.IsZero() || meta.UpdatedAt.IsZero() {
		t.Fatal("missing timestamps")
	}

	if err := b.Delete(ctx, "reports/2026/summary.json"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, _, err := b.Get(ctx, "reports/2026/summary.json"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestBlobReplaceChargesOnlyTheDelta(t *testing.T) {
	ctx := context.Background()
	store, b := newBlobs(t, 1<<20, 100)

	if _, err := b.Put(ctx, "a", bytes.NewReader(make([]byte, 80)), ""); err != nil {
		t.Fatalf("first put: %v", err)
	}
	// Replacing 80 bytes with 90 must succeed: the quota charges the delta, not the sum.
	if _, err := b.Put(ctx, "a", bytes.NewReader(make([]byte, 90)), ""); err != nil {
		t.Fatalf("replace: %v", err)
	}

	// The superseded physical file must be gone, leaving exactly one object.
	if n := countObjects(t, store); n != 1 {
		t.Fatalf("%d physical objects after replace, want 1", n)
	}
}

func TestBlobQuotaAndSizeLimits(t *testing.T) {
	ctx := context.Background()
	_, b := newBlobs(t, 50, 120)

	if _, err := b.Put(ctx, "big", bytes.NewReader(make([]byte, 51)), ""); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got %v, want ErrTooLarge", err)
	}
	if _, err := b.Put(ctx, "a", bytes.NewReader(make([]byte, 50)), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Put(ctx, "b", bytes.NewReader(make([]byte, 50)), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Put(ctx, "c", bytes.NewReader(make([]byte, 50)), ""); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("got %v, want ErrQuotaExceeded", err)
	}
	// The rejected write must not have disturbed anything.
	rc, _, err := b.Get(ctx, "a")
	if err != nil {
		t.Fatalf("existing blob damaged: %v", err)
	}
	rc.Close()
}

func TestFailedPutLeavesTheExistingBlob(t *testing.T) {
	ctx := context.Background()
	store, b := newBlobs(t, 50, 60)

	if _, err := b.Put(ctx, "a", strings.NewReader("original"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Put(ctx, "a", bytes.NewReader(make([]byte, 51)), ""); err == nil {
		t.Fatal("expected the oversized replacement to fail")
	}

	rc, _, err := b.Get(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(rc)
	rc.Close()
	if string(body) != "original" {
		t.Fatalf("body = %q, want the original", body)
	}
	if n := countObjects(t, store); n != 1 {
		t.Fatalf("%d physical objects after a failed put, want 1", n)
	}
}

func TestScopesAreIsolated(t *testing.T) {
	ctx := context.Background()
	store, a := newBlobs(t, 1<<20, 1<<20)
	b := store.Scoped("other")

	if _, err := a.Put(ctx, "shared", strings.NewReader("from a"), ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Get(ctx, "shared"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("scope leak: got %v, want ErrNotFound", err)
	}
	if _, err := b.Put(ctx, "shared", strings.NewReader("from b"), ""); err != nil {
		t.Fatal(err)
	}
	rc, _, err := a.Get(ctx, "shared")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(rc)
	rc.Close()
	if string(body) != "from a" {
		t.Fatalf("scope a sees %q", body)
	}
}

func TestBlobKeyValidation(t *testing.T) {
	ctx := context.Background()
	_, b := newBlobs(t, 1<<20, 1<<20)

	bad := []string{
		"", "/absolute", "..", "../escape", "a//b", "a/../b", "back\\slash",
		"per%2fcent", ".hidden", "-leading", "a/\x00b", strings.Repeat("x", 600),
		strings.Repeat("y", 129),
	}
	for _, key := range bad {
		if _, err := b.Put(ctx, key, strings.NewReader("x"), ""); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Put(%q) = %v, want ErrInvalidKey", key, err)
		}
	}

	good := []string{"a", "a/b/c", "A1/b-2.json", "x_y/z.tar.gz"}
	for _, key := range good {
		if _, err := b.Put(ctx, key, strings.NewReader("x"), ""); err != nil {
			t.Errorf("Put(%q) = %v, want success", key, err)
		}
	}
}

func TestMIMENormalization(t *testing.T) {
	cases := map[string]string{
		"":                          "application/octet-stream",
		"  ":                        "application/octet-stream",
		"text/plain; charset=utf-8": "text/plain",
		"IMAGE/PNG":                 "image/png",
		"nonsense":                  "application/octet-stream",
		"a/<script>":                "application/octet-stream",
		"application/vnd.api+json":  "application/vnd.api+json",
	}
	for in, want := range cases {
		if got := normalizeMIME(in); got != want {
			t.Errorf("normalizeMIME(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRecoverRemovesOrphansAndStagedFiles(t *testing.T) {
	ctx := context.Background()
	store, b := newBlobs(t, 1<<20, 1<<20)

	if _, err := b.Put(ctx, "keep", strings.NewReader("keep me"), ""); err != nil {
		t.Fatal(err)
	}

	// Simulate a crash: a staged temp file and an orphaned physical object.
	tmp := filepath.Join(store.opts.Dir, "tmp", "staging-crashed")
	if err := os.WriteFile(tmp, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	orphanDir := filepath.Join(store.opts.Dir, "objects", "ff")
	if err := os.MkdirAll(orphanDir, 0o700); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(orphanDir, "orphaned")
	if err := os.WriteFile(orphan, []byte("nobody references me"), 0o600); err != nil {
		t.Fatal(err)
	}

	removed, err := store.Recover(ctx)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed %d files, want 2", removed)
	}
	if _, err := os.Stat(tmp); !errors.Is(err, os.ErrNotExist) {
		t.Error("staged file survived recovery")
	}
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Error("orphaned object survived recovery")
	}
	rc, _, err := b.Get(ctx, "keep")
	if err != nil {
		t.Errorf("recovery deleted a referenced blob: %v", err)
	} else {
		rc.Close()
	}
}

func TestMutationAdmissionRejectsPutAndDelete(t *testing.T) {
	ctx := context.Background()
	store, plain := newBlobs(t, 1<<20, 1<<20)

	if _, err := plain.Put(ctx, "existing", strings.NewReader("here"), ""); err != nil {
		t.Fatal(err)
	}

	denied := errors.New("plugin disabled")
	gated := WithMutationAdmission(store.Scoped("demo"), func(context.Context, Tx) error {
		return denied
	})

	if _, err := gated.Put(ctx, "new", strings.NewReader("x"), ""); !errors.Is(err, denied) {
		t.Fatalf("put: got %v, want the admission error", err)
	}
	if err := gated.Delete(ctx, "existing"); !errors.Is(err, denied) {
		t.Fatalf("delete: got %v, want the admission error", err)
	}

	// Reads stay available while mutations are refused.
	rc, _, err := gated.Get(ctx, "existing")
	if err != nil {
		t.Fatalf("get should remain available: %v", err)
	}
	rc.Close()
	// The rejected Put must leave no physical file behind.
	if n := countObjects(t, store); n != 1 {
		t.Fatalf("%d physical objects after a rejected put, want 1", n)
	}
}

func TestURLIsAnApplicationRoute(t *testing.T) {
	_, b := newBlobs(t, 1<<20, 1<<20)
	got := b.URL("reports/x.json")
	if got != "/api/blobs/demo/reports/x.json" {
		t.Fatalf("URL = %q", got)
	}
}

func countObjects(t *testing.T, store *BlobStore) int {
	t.Helper()
	n := 0
	root := filepath.Join(store.opts.Dir, "objects")
	shards, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, shard := range shards {
		files, err := os.ReadDir(filepath.Join(root, shard.Name()))
		if err != nil {
			t.Fatal(err)
		}
		n += len(files)
	}
	return n
}
