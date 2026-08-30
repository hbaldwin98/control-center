// Package storage owns the SQLite connection, transactions, migrations, and binary blobs
// on the filesystem.
//
// Layer 0. It imports no other core module.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"time"
)

// DB and Tx expose the same SQL operations needed by module repositories. A Tx is valid
// only for the callback that receives it and must not be retained or used concurrently.
type DB interface {
	Query(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, q string, args ...any) *sql.Row
	Exec(ctx context.Context, q string, args ...any) (sql.Result, error)
	Tx(ctx context.Context, fn func(Tx) error) error
}

// Tx is a handle on an open write transaction. It is only valid for the duration of the
// callback that received it.
type Tx interface {
	Query(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, q string, args ...any) *sql.Row
	Exec(ctx context.Context, q string, args ...any) (sql.Result, error)
}

// Migrator applies a namespace's migrations idempotently and in order.
type Migrator interface {
	Apply(namespace string, migrations []Migration) error
}

// Migration is one forward-only schema step within a namespace.
type Migration struct {
	Version int
	Name    string
	Up      string
}

// Blobs stores binary objects on the filesystem with authoritative metadata in SQLite.
type Blobs interface {
	Put(ctx context.Context, key string, r io.Reader, mime string) (BlobRef, error)
	Get(ctx context.Context, key string) (io.ReadCloser, BlobMeta, error)
	Delete(ctx context.Context, key string) error
	// URL returns an authenticated application route, never a filesystem path.
	URL(key string) string
}

// BlobRef identifies a stored object and carries its integrity metadata.
type BlobRef struct {
	Key    string `json:"key"`
	MIME   string `json:"mime"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// BlobMeta is a BlobRef plus its storage timestamps.
type BlobMeta struct {
	BlobRef
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Errors returned across the storage boundary.
var (
	// ErrNestedTx is returned when a transaction callback opens another transaction.
	// The single writer connection is already held, so this would deadlock.
	ErrNestedTx = errors.New("storage: nested transaction")

	// ErrNotFound is returned by blob reads for an unknown key.
	ErrNotFound = errors.New("storage: not found")

	// ErrInvalidKey is returned for a blob key that fails validation.
	ErrInvalidKey = errors.New("storage: invalid blob key")

	// ErrTooLarge is returned when a blob exceeds the per-object size limit.
	ErrTooLarge = errors.New("storage: object exceeds size limit")

	// ErrQuotaExceeded is returned when a blob write would exceed the scope quota.
	ErrQuotaExceeded = errors.New("storage: scope quota exceeded")
)
