# storage

**Layer 0** · `internal/core/storage` · imports nothing · used by every module

**Responsibility.** Own the SQLite connection and migrations, and store binary blobs on
the filesystem.

---

## Interface

Two surfaces, deliberately kept in one module: they are both "where bytes live," and
splitting them would double the module count for no gain in clarity.

```go
package storage

// --- SQL ---

type DB interface {
    Query(ctx context.Context, q string, args ...any) (*sql.Rows, error)
    Exec(ctx context.Context, q string, args ...any) (sql.Result, error)
    Tx(ctx context.Context, fn func(Tx) error) error
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

// --- Blobs ---

type Blobs interface {
    Put(ctx context.Context, key string, r io.Reader, mime string) (BlobRef, error)
    Get(ctx context.Context, key string) (io.ReadCloser, *BlobMeta, error)
    Delete(ctx context.Context, key string) error
    URL(key string) string // served under /blobs/<scope>/<key>
}

type BlobRef struct {
    Key  string
    MIME string
    Size int64
}
```

---

## Namespacing

Every table carries a prefix naming its owner:

| Owner | Prefix | Example |
|---|---|---|
| Core modules | `core_` | `core_events`, `core_jobs` |
| A plugin | `<plugin_id>_` | `bidrl_lots`, `bidrl_auctions` |

`Migrator.Apply` takes the namespace and rejects migrations that create tables outside it.

A plugin's `Store()` handle is scoped the same way. This is a guardrail against mistakes,
not a security boundary — plugins are trusted code.

---

## Tables

```
core_migrations(namespace, version, name, applied_at)
```

---

## Notes

- **Blobs are files, not rows.** Image caches would bloat the database file and make
  backups painful. Blobs live under a data directory, addressed by key.
- **One SQLite file**, WAL mode, with a single writer. At this scale contention is not a
  concern, and a single file makes backup a copy.
- Blob keys are namespaced by plugin, exactly like tables.
