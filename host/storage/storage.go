// Package storage is the plugin-facing SQL and blob surface.
//
// Query returns a Rows interface rather than *sql.Rows so a prefix authorizer can
// check out a connection for the query's lifetime and return it on Close — the seam
// that also lets a plugin move out of process later.
package storage

import (
	"context"
	"io"
	"time"
)

// DB is SQL restricted to this plugin's table prefix.
type DB interface {
	Query(ctx context.Context, q string, args ...any) (Rows, error)
	QueryRow(ctx context.Context, q string, args ...any) Row
	Exec(ctx context.Context, q string, args ...any) (Result, error)
	Tx(ctx context.Context, fn func(Tx) error) error
}

// Tx is valid only for the callback that receives it.
type Tx interface {
	Query(ctx context.Context, q string, args ...any) (Rows, error)
	QueryRow(ctx context.Context, q string, args ...any) Row
	Exec(ctx context.Context, q string, args ...any) (Result, error)
	AfterCommit(fn func())
}

// Rows is one result set. Close returns the underlying connection.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Close() error
	Err() error
	Columns() ([]string, error)
}

// Row is a single-row result. Scan is the only operation; it releases the connection.
type Row interface {
	Scan(dest ...any) error
}

// Result is the outcome of Exec.
type Result interface {
	LastInsertId() (int64, error)
	RowsAffected() (int64, error)
}

// Blobs is binary storage namespaced to this plugin.
type Blobs interface {
	Put(ctx context.Context, key string, r io.Reader, mime string) (BlobRef, error)
	Get(ctx context.Context, key string) (io.ReadCloser, BlobMeta, error)
	Delete(ctx context.Context, key string) error
	URL(key string) string
}

// BlobRef identifies a stored object.
type BlobRef struct {
	Key    string
	MIME   string
	Size   int64
	SHA256 string
}

// BlobMeta is a BlobRef plus storage timestamps.
type BlobMeta struct {
	BlobRef
	CreatedAt time.Time
	UpdatedAt time.Time
}
