package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Blob keys are one or more "/"-separated ASCII segments. Each segment matches
// [A-Za-z0-9][A-Za-z0-9._-]{0,127} and the complete key is at most 512 bytes. Absolute
// paths, empty segments, ".", "..", backslashes, control characters, and percent-encoded
// path syntax are rejected before any filesystem access.
const maxKeyBytes = 512

// BlobOptions bounds filesystem blob storage. Limits must be finite.
type BlobOptions struct {
	Dir            string
	MaxObjectBytes int64
	MaxScopeBytes  int64

	// URLPrefix is the authenticated application route blobs are served from. The
	// handler authorizes the scope before opening the file.
	URLPrefix string
}

// BlobStore owns the blob directory and the metadata rows in core_blobs. Callers never
// use it directly; they receive a Scoped handle that prepends its own namespace, so they
// cannot address another scope.
type BlobStore struct {
	db   DB
	opts BlobOptions
}

// NewBlobStore validates the options and prepares the blob directory.
func NewBlobStore(db DB, opts BlobOptions) (*BlobStore, error) {
	if opts.Dir == "" {
		return nil, errors.New("storage: empty blob directory")
	}
	if opts.MaxObjectBytes <= 0 || opts.MaxScopeBytes <= 0 {
		return nil, errors.New("storage: blob limits must be finite and positive")
	}
	if opts.URLPrefix == "" {
		opts.URLPrefix = "/api/blobs"
	}
	if err := os.MkdirAll(filepath.Join(opts.Dir, "tmp"), 0o700); err != nil {
		return nil, fmt.Errorf("storage: create blob dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(opts.Dir, "objects"), 0o700); err != nil {
		return nil, fmt.Errorf("storage: create blob dir: %w", err)
	}
	return &BlobStore{db: db, opts: opts}, nil
}

// Scoped returns the handle a single owner sees. The scope is a core module name or a
// plugin ID; the ownership rule is applied here rather than trusted to the caller.
func (b *BlobStore) Scoped(scope string) Blobs {
	return &scopedBlobs{store: b, scope: scope}
}

// Recover deletes staged temporary files and physical objects no metadata row references.
// A crash therefore exposes either the old blob or the new blob, never a partial file.
func (b *BlobStore) Recover(ctx context.Context) (removed int, err error) {
	tmpDir := filepath.Join(b.opts.Dir, "tmp")
	entries, err := os.ReadDir(tmpDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	for _, e := range entries {
		if err := os.Remove(filepath.Join(tmpDir, e.Name())); err == nil {
			removed++
		}
	}

	referenced := make(map[string]struct{})
	rows, err := b.db.Query(ctx, `SELECT physical_name FROM core_blobs`)
	if err != nil {
		return removed, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return removed, err
		}
		referenced[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return removed, err
	}

	objDir := filepath.Join(b.opts.Dir, "objects")
	shards, err := os.ReadDir(objDir)
	if err != nil {
		return removed, err
	}
	for _, shard := range shards {
		if !shard.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(objDir, shard.Name()))
		if err != nil {
			return removed, err
		}
		for _, f := range files {
			name := shard.Name() + "/" + f.Name()
			if _, ok := referenced[name]; ok {
				continue
			}
			if err := os.Remove(filepath.Join(objDir, shard.Name(), f.Name())); err == nil {
				removed++
			}
		}
	}
	return removed, nil
}

func (b *BlobStore) physicalPath(name string) string {
	return filepath.Join(b.opts.Dir, "objects", filepath.FromSlash(name))
}

// scopedBlobs is one owner's view. It prepends its scope to every operation.
type scopedBlobs struct {
	store *BlobStore
	scope string

	// admit runs inside the metadata transaction of Put and Delete, after staging and
	// before the logical key changes. pluginhost uses it to check policy.
	admit func(context.Context, Tx) error
}

var _ Blobs = (*scopedBlobs)(nil)

// WithMutationAdmission is an internal composition hook. Put and Delete invoke admit in
// their metadata transaction after staging and before changing the logical key, so a
// mutation either commits before a concurrent disable or is rejected after it.
func WithMutationAdmission(b Blobs, admit func(context.Context, Tx) error) Blobs {
	sb, ok := b.(*scopedBlobs)
	if !ok {
		return b
	}
	clone := *sb
	clone.admit = admit
	return &clone
}

func (s *scopedBlobs) URL(key string) string {
	return s.store.opts.URLPrefix + "/" + s.scope + "/" + key
}

// Put streams r to a temporary file on the destination filesystem, computes its size and
// digest, renames it to a new immutable physical name, and then atomically swaps the
// logical key's metadata. Replacing a key charges only the size delta. Exceeding the
// per-object limit or the scope quota aborts without changing the existing blob.
func (s *scopedBlobs) Put(ctx context.Context, key string, r io.Reader, mime string) (BlobRef, error) {
	if err := validateBlobKey(key); err != nil {
		return BlobRef{}, err
	}
	mime = normalizeMIME(mime)

	// A streaming allowance so an oversized body is cut off rather than written in full.
	// The transaction below re-checks both limits authoritatively.
	existing, scopeTotal, err := s.usage(ctx, key)
	if err != nil {
		return BlobRef{}, err
	}
	allowance := s.store.opts.MaxScopeBytes - (scopeTotal - existing)
	if allowance > s.store.opts.MaxObjectBytes {
		allowance = s.store.opts.MaxObjectBytes
	}
	if allowance < 0 {
		allowance = 0
	}

	staged, err := s.stage(ctx, r, allowance)
	if err != nil {
		return BlobRef{}, err
	}

	physical := staged.physical
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(s.store.physicalPath(physical))
		}
	}()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	var oldPhysical string

	err = s.store.db.Tx(ctx, func(tx Tx) error {
		var prevSize int64
		var prevPhysical string
		err := tx.QueryRow(ctx,
			`SELECT physical_name, size FROM core_blobs WHERE scope = ? AND key = ?`,
			s.scope, key).Scan(&prevPhysical, &prevSize)
		if err != nil && !IsNoRows(err) {
			return err
		}

		var total int64
		if err := tx.QueryRow(ctx,
			`SELECT coalesce(sum(size), 0) FROM core_blobs WHERE scope = ?`, s.scope).
			Scan(&total); err != nil {
			return err
		}
		if staged.size > s.store.opts.MaxObjectBytes {
			return ErrTooLarge
		}
		if total-prevSize+staged.size > s.store.opts.MaxScopeBytes {
			return ErrQuotaExceeded
		}

		// The admission point: after staging, before the logical key changes.
		if s.admit != nil {
			if err := s.admit(ctx, tx); err != nil {
				return err
			}
		}

		_, err = tx.Exec(ctx,
			`INSERT INTO core_blobs(scope, key, physical_name, mime, size, sha256, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(scope, key) DO UPDATE SET
			     physical_name = excluded.physical_name,
			     mime          = excluded.mime,
			     size          = excluded.size,
			     sha256        = excluded.sha256,
			     updated_at    = excluded.updated_at`,
			s.scope, key, physical, mime, staged.size, staged.sum, now, now)
		if err != nil {
			return err
		}
		oldPhysical = prevPhysical
		return nil
	})
	if err != nil {
		return BlobRef{}, err
	}
	committed = true

	// The old physical file is unreferenced once the swap commits. A failure here leaves
	// garbage that Recover collects at startup, never a dangling reference.
	if oldPhysical != "" && oldPhysical != physical {
		_ = os.Remove(s.store.physicalPath(oldPhysical))
	}

	return BlobRef{Key: key, MIME: mime, Size: staged.size, SHA256: staged.sum}, nil
}

func (s *scopedBlobs) Get(ctx context.Context, key string) (io.ReadCloser, BlobMeta, error) {
	if err := validateBlobKey(key); err != nil {
		return nil, BlobMeta{}, err
	}
	var physical, mime, sum, created, updated string
	var size int64
	err := s.store.db.QueryRow(ctx,
		`SELECT physical_name, mime, size, sha256, created_at, updated_at
		   FROM core_blobs WHERE scope = ? AND key = ?`, s.scope, key).
		Scan(&physical, &mime, &size, &sum, &created, &updated)
	if IsNoRows(err) {
		return nil, BlobMeta{}, ErrNotFound
	}
	if err != nil {
		return nil, BlobMeta{}, err
	}

	f, err := os.Open(s.store.physicalPath(physical))
	if err != nil {
		return nil, BlobMeta{}, fmt.Errorf("storage: open blob %s/%s: %w", s.scope, key, err)
	}
	meta := BlobMeta{BlobRef: BlobRef{Key: key, MIME: mime, Size: size, SHA256: sum}}
	meta.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	meta.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return f, meta, nil
}

func (s *scopedBlobs) Delete(ctx context.Context, key string) error {
	if err := validateBlobKey(key); err != nil {
		return err
	}
	var physical string
	err := s.store.db.Tx(ctx, func(tx Tx) error {
		err := tx.QueryRow(ctx,
			`SELECT physical_name FROM core_blobs WHERE scope = ? AND key = ?`,
			s.scope, key).Scan(&physical)
		if IsNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if s.admit != nil {
			if err := s.admit(ctx, tx); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `DELETE FROM core_blobs WHERE scope = ? AND key = ?`, s.scope, key)
		return err
	})
	if err != nil {
		return err
	}
	_ = os.Remove(s.store.physicalPath(physical))
	return nil
}

// usage reports the current size of one key and the scope's total, for the streaming
// allowance. The authoritative check happens in the metadata transaction.
func (s *scopedBlobs) usage(ctx context.Context, key string) (existing, total int64, err error) {
	if err := s.store.db.QueryRow(ctx,
		`SELECT coalesce(sum(size), 0) FROM core_blobs WHERE scope = ?`, s.scope).
		Scan(&total); err != nil {
		return 0, 0, err
	}
	err = s.store.db.QueryRow(ctx,
		`SELECT size FROM core_blobs WHERE scope = ? AND key = ?`, s.scope, key).Scan(&existing)
	if IsNoRows(err) {
		return 0, total, nil
	}
	return existing, total, err
}

type stagedBlob struct {
	physical string
	size     int64
	sum      string
}

// stage writes at most allowance bytes to a temporary file on the destination filesystem,
// flushes it, and renames it to a new immutable physical name.
func (s *scopedBlobs) stage(ctx context.Context, r io.Reader, allowance int64) (stagedBlob, error) {
	tmp, err := os.CreateTemp(filepath.Join(s.store.opts.Dir, "tmp"), "staging-*")
	if err != nil {
		return stagedBlob{}, fmt.Errorf("storage: stage blob: %w", err)
	}
	tmpName := tmp.Name()
	done := false
	defer func() {
		tmp.Close()
		if !done {
			_ = os.Remove(tmpName)
		}
	}()

	h := sha256.New()
	// One extra byte distinguishes "exactly at the limit" from "over it".
	limited := io.LimitReader(r, allowance+1)
	size, err := io.Copy(io.MultiWriter(tmp, h), limited)
	if err != nil {
		return stagedBlob{}, err
	}
	if size > allowance {
		if allowance >= s.store.opts.MaxObjectBytes {
			return stagedBlob{}, ErrTooLarge
		}
		return stagedBlob{}, ErrQuotaExceeded
	}
	if err := ctx.Err(); err != nil {
		return stagedBlob{}, err
	}
	if err := tmp.Sync(); err != nil {
		return stagedBlob{}, err
	}
	if err := tmp.Close(); err != nil {
		return stagedBlob{}, err
	}

	physical, err := newPhysicalName()
	if err != nil {
		return stagedBlob{}, err
	}
	dest := s.store.physicalPath(physical)
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return stagedBlob{}, err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return stagedBlob{}, fmt.Errorf("storage: publish blob: %w", err)
	}
	done = true

	return stagedBlob{physical: physical, size: size, sum: hex.EncodeToString(h.Sum(nil))}, nil
}

// newPhysicalName returns a fresh immutable name, sharded so one directory does not grow
// without bound.
func newPhysicalName() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	name := hex.EncodeToString(b)
	return name[:2] + "/" + name[2:], nil
}

// validateBlobKey enforces the key grammar before any filesystem access.
func validateBlobKey(key string) error {
	if key == "" || len(key) > maxKeyBytes {
		return fmt.Errorf("%w: length", ErrInvalidKey)
	}
	if strings.ContainsAny(key, `\%`) || strings.HasPrefix(key, "/") {
		return fmt.Errorf("%w: %q", ErrInvalidKey, key)
	}
	for _, seg := range strings.Split(key, "/") {
		if err := validateSegment(seg); err != nil {
			return err
		}
	}
	// Defence in depth: the grammar already excludes "." and "..", but a cleaned key that
	// differs from the original means something slipped through.
	if path.Clean(key) != key {
		return fmt.Errorf("%w: %q is not canonical", ErrInvalidKey, key)
	}
	return nil
}

func validateSegment(seg string) error {
	if seg == "" || len(seg) > 128 {
		return fmt.Errorf("%w: segment length", ErrInvalidKey)
	}
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			continue
		case i > 0 && (c == '.' || c == '_' || c == '-'):
			continue
		default:
			return fmt.Errorf("%w: segment %q", ErrInvalidKey, seg)
		}
	}
	return nil
}

// normalizeMIME keeps the stored type to a safe, validated value. The serving handler
// echoes it with nosniff, so an unparseable type becomes the generic binary type rather
// than something a browser might interpret.
func normalizeMIME(mime string) string {
	mime = strings.TrimSpace(strings.ToLower(mime))
	if mime == "" {
		return "application/octet-stream"
	}
	base, _, _ := strings.Cut(mime, ";")
	base = strings.TrimSpace(base)
	typ, sub, ok := strings.Cut(base, "/")
	if !ok || typ == "" || sub == "" {
		return "application/octet-stream"
	}
	for _, part := range []string{typ, sub} {
		for i := 0; i < len(part); i++ {
			c := part[i]
			ok := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
				c == '.' || c == '-' || c == '+'
			if !ok {
				return "application/octet-stream"
			}
		}
	}
	return typ + "/" + sub
}

// InlineImageMIMEs is the explicit allowlist the serving handler may render inline. SVG,
// HTML, and other active content stay attachments.
var InlineImageMIMEs = map[string]struct{}{
	"image/png":  {},
	"image/jpeg": {},
	"image/gif":  {},
	"image/webp": {},
	"image/avif": {},
}
