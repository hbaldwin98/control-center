# storage

**Layer 0** · `internal/core/storage` · imports nothing · used by every module

**Responsibility.** Own the SQLite connection, transactions, migrations, coordinated
backups, and binary blobs on the filesystem.

---

## Interface

```go
package storage

// DB and Tx expose the same SQL operations needed by module repositories. A Tx is valid
// only for the callback that receives it and must not be retained or used concurrently.
type DB interface {
    Query(ctx context.Context, q string, args ...any) (*sql.Rows, error)
    QueryRow(ctx context.Context, q string, args ...any) *sql.Row
    Exec(ctx context.Context, q string, args ...any) (sql.Result, error)
    Tx(ctx context.Context, fn func(Tx) error) error
}

type Tx interface {
    Query(ctx context.Context, q string, args ...any) (*sql.Rows, error)
    QueryRow(ctx context.Context, q string, args ...any) *sql.Row
    Exec(ctx context.Context, q string, args ...any) (sql.Result, error)

    // AfterCommit runs fn after a successful commit, once the write lock is
    // released, so the callback may open a new transaction. A rollback
    // discards registered callbacks. They must not use the Tx handle.
    AfterCommit(fn func())
}

type Migrator interface {
    // Apply runs migrations idempotently, in order, recording what ran.
    Apply(namespace string, migrations []Migration) error
}

type Migration struct {
    Version int
    Name    string
    Up      string
}

type Blobs interface {
    Put(ctx context.Context, key string, r io.Reader, mime string) (BlobRef, error)
    Get(ctx context.Context, key string) (io.ReadCloser, BlobMeta, error)
    Delete(ctx context.Context, key string) error
    URL(key string) string // authenticated application route, never a filesystem URL
}

// WithMutationAdmission is an internal composition hook. Put/Delete invoke admit in
// their metadata transaction after staging and before changing the logical key.
func WithMutationAdmission(b Blobs, admit func(context.Context, Tx) error) Blobs

type BlobRef struct {
    Key    string
    MIME   string
    Size   int64
    SHA256 string
}

type BlobMeta struct {
    BlobRef
    CreatedAt time.Time
    UpdatedAt time.Time
}
```

`DB.Tx` commits only when the callback returns `nil`; errors, cancellation, and panics
roll it back. `AfterCommit` is how a module that joined a caller's transaction can notify
watchers only after that caller commits — policy uses it so `ExceedDisable` and an
accounting-invariant disable cancel admitted contexts without deadlocking on the writer.
Module methods that must join a caller's transaction accept `storage.Tx` rather than
opening a nested transaction. This is the boundary used to write an AI usage row, settle
its policy reservation, and insert its event atomically.

The application uses WAL mode, a finite busy timeout, one serialized writer connection,
and a bounded read pool. A transaction callback may not nest `DB.Tx`; cancellation while
waiting for the writer returns the context error. These rules make write ordering and
shutdown behavior explicit rather than relying on SQLite's default lock errors.

---

## Namespacing

Every table carries a prefix naming its owner:

| Owner | Prefix | Example |
|---|---|---|
| Core modules | `core_` | `core_events`, `core_jobs` |
| A plugin | `<plugin_id>_` | `bidrl_lots`, `bidrl_auctions` |

`Migrator.Apply` serializes callers and applies each migration in its own transaction.
Versions are positive and strictly increasing within a namespace. The migration table
stores a SHA-256 checksum of name plus SQL; an already-applied version with a different
name or checksum, a duplicate version, or a gap fails startup for that owner. A failed
migration rolls back completely and is not recorded.

Plugin migration and runtime SQL execute with a SQLite authorizer that rejects reads and
writes outside the plugin's table prefix, plus `ATTACH`, `DETACH`, temporary objects,
unsafe pragmas, and triggers or views that reference another namespace. Identifiers are
validated after SQLite parsing rather than by scanning SQL text. The scoped blob handle
applies the same ownership rule. These are guardrails against mistakes; plugins remain
trusted code.

The pluginhost wrapper checks policy in the same transaction before a plugin mutation.
Queries remain available while disabled so the host UI can display retained state;
plugin `Exec`, `Tx`, blob `Put`/`Delete`, and event publication are rejected after disable
unless their transaction was admitted before the disable transition.

---

## Blobs

A blob key is one or more `/`-separated ASCII segments. Each segment must match
`[A-Za-z0-9][A-Za-z0-9._-]{0,127}`; the complete key is at most 512 bytes. Absolute
paths, empty segments, `.`, `..`, backslashes, control characters, and percent-encoded
path syntax are rejected before filesystem access. The scoped handle prepends its own
namespace, so callers cannot choose another scope.

`Put` enforces a configured per-object size limit and per-scope byte quota while
streaming. Limits must be finite; the v1 defaults are 64 MiB per object and 2 GiB per
scope. Replacing a key charges only the size delta. Exceeding either limit aborts without
changing the existing blob.

Writes use a temporary file on the destination filesystem, compute size and SHA-256,
flush it, and rename it to a new immutable physical name. A database transaction then
atomically swaps the logical key's metadata and quota accounting. The old physical file
is removed after commit. Startup recovery deletes temporary and unreferenced physical
files, so a crash exposes either the old blob or the new blob, never a partial file.

Blob metadata is authoritative and persisted in SQLite:

```
core_blobs(scope, key, physical_name, mime, size, sha256, created_at, updated_at)
```

`URL` returns an application route. The serving handler authenticates the request and
authorizes access to the blob's scope before opening it. Responses use the validated
stored MIME type, `X-Content-Type-Options: nosniff`, and `Content-Disposition:
attachment` by default. Only an explicit safe-image allowlist may be served inline; SVG,
HTML, and other active content remain attachments.

---

## Backup

WAL mode does not make copying the main database file safe. The backup coordinator takes
an application snapshot barrier that drains and blocks SQL and blob mutations, checkpoints
the WAL, copies SQLite through its online backup API, and copies every physical blob
referenced by that database snapshot before releasing the barrier. The database backup
and blob directory are published together as one snapshot. Restore verifies blob sizes
and hashes before making the snapshot active.

---

## Tables

```
core_migrations(namespace, version, name, checksum, applied_at)
core_blobs(scope, key, physical_name, mime, size, sha256, created_at, updated_at)
```

Blobs remain files rather than row payloads; SQLite stores their identity, integrity, and
quota metadata.
