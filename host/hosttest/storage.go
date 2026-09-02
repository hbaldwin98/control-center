package hosttest

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	hostpolicy "github.com/hbaldwin98/control-center/host/policy"
	hoststorage "github.com/hbaldwin98/control-center/host/storage"
)

// db is the plugin-facing SQL surface backed by a private in-memory SQLite database.
//
// The real host isolates plugins with a SQLite authorizer that rejects any statement
// touching a table outside the plugin's prefix. Reproducing that here would mean
// reaching into the driver's internals, so the double isolates by database instead:
// each plugin gets its own, which makes a cross-plugin read impossible rather than
// merely rejected. The prefix rule itself is still enforced, at the point where a
// plugin can get it wrong -- the DDL it hands to Migrate.
type db struct {
	sqlDB  *sql.DB
	prefix string
	gate   func(mutating bool) error
}

func (d *db) Query(ctx context.Context, q string, args ...any) (hoststorage.Rows, error) {
	if err := d.gate(mutating(q)); err != nil {
		return nil, err
	}
	rows, err := d.sqlDB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (d *db) QueryRow(ctx context.Context, q string, args ...any) hoststorage.Row {
	if err := d.gate(mutating(q)); err != nil {
		return errRow{err}
	}
	return d.sqlDB.QueryRowContext(ctx, q, args...)
}

func (d *db) Exec(ctx context.Context, q string, args ...any) (hoststorage.Result, error) {
	if err := d.gate(mutating(q)); err != nil {
		return nil, err
	}
	return d.sqlDB.ExecContext(ctx, q, args...)
}

func (d *db) Tx(ctx context.Context, fn func(hoststorage.Tx) error) error {
	if err := d.gate(true); err != nil {
		return err
	}
	sqlTx, err := d.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	t := &tx{tx: sqlTx, gate: d.gate}
	if err := fn(t); err != nil {
		_ = sqlTx.Rollback()
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		return err
	}
	// AfterCommit callbacks run only once the data is durable, in registration order,
	// the way the real host runs them.
	for _, f := range t.after {
		f()
	}
	return nil
}

// tx is one transaction. It is valid only for the callback that receives it; calling
// it afterwards fails the same way the real host's does, because the underlying
// *sql.Tx is already terminal.
type tx struct {
	tx    *sql.Tx
	gate  func(mutating bool) error
	after []func()
}

func (t *tx) Query(ctx context.Context, q string, args ...any) (hoststorage.Rows, error) {
	rows, err := t.tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (t *tx) QueryRow(ctx context.Context, q string, args ...any) hoststorage.Row {
	return t.tx.QueryRowContext(ctx, q, args...)
}

func (t *tx) Exec(ctx context.Context, q string, args ...any) (hoststorage.Result, error) {
	return t.tx.ExecContext(ctx, q, args...)
}

func (t *tx) AfterCommit(fn func()) {
	if fn != nil {
		t.after = append(t.after, fn)
	}
}

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

// mutating reports whether a statement writes. The gate only needs to tell reads from
// writes, because a disabled plugin may still read its own tables.
func mutating(q string) bool {
	switch strings.ToLower(firstWord(q)) {
	case "select", "with", "explain", "pragma", "":
		return false
	default:
		return true
	}
}

func firstWord(q string) string {
	q = strings.TrimSpace(stripSQLComments(q))
	i := strings.IndexFunc(q, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '('
	})
	if i < 0 {
		return q
	}
	return q[:i]
}

var sqlLineComment = regexp.MustCompile(`(?m)--.*$`)
var sqlBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)

func stripSQLComments(q string) string {
	q = sqlLineComment.ReplaceAllString(q, "")
	return sqlBlockComment.ReplaceAllString(q, "")
}

// createdObject matches the name a CREATE statement defines, so Migrate can hold a
// plugin to the prefix rule the real host enforces at runtime.
var createdObject = regexp.MustCompile(`(?is)\bcreate\s+(?:unique\s+)?(?:temp\s+|temporary\s+)?(table|index|view|trigger|virtual\s+table)\s+(?:if\s+not\s+exists\s+)?["'` + "`" + `\[]?([a-z0-9_]+)`)

// checkMigrationPrefix rejects DDL that names an object outside the plugin's prefix.
// Every table a plugin creates must start with its ID and an underscore.
func checkMigrationPrefix(prefix, sql string) error {
	for _, m := range createdObject.FindAllStringSubmatch(stripSQLComments(sql), -1) {
		name := strings.ToLower(m[2])
		if !strings.HasPrefix(name, prefix) {
			return fmt.Errorf("hosttest: migration creates %s %q outside the plugin prefix %q", strings.ToLower(m[1]), name, prefix)
		}
	}
	return nil
}

// blobs is binary storage namespaced to one plugin, held in memory.
type blobs struct {
	mu    sync.Mutex
	items map[string]*blob
	gate  func(mutating bool) error
	now   func() time.Time
	id    string
}

type blob struct {
	meta hoststorage.BlobMeta
	body []byte
}

func (b *blobs) Put(ctx context.Context, key string, r io.Reader, mime string) (hoststorage.BlobRef, error) {
	if err := b.gate(true); err != nil {
		return hoststorage.BlobRef{}, err
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return hoststorage.BlobRef{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	ref := hoststorage.BlobRef{Key: key, MIME: mime, Size: int64(len(body)), SHA256: sha256Hex(body)}
	existing, ok := b.items[key]
	created := now
	if ok {
		created = existing.meta.CreatedAt
	}
	b.items[key] = &blob{
		meta: hoststorage.BlobMeta{BlobRef: ref, CreatedAt: created, UpdatedAt: now},
		body: body,
	}
	return ref, nil
}

func (b *blobs) Get(ctx context.Context, key string) (io.ReadCloser, hoststorage.BlobMeta, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	item, ok := b.items[key]
	if !ok {
		return nil, hoststorage.BlobMeta{}, fmt.Errorf("hosttest: no blob %q", key)
	}
	return io.NopCloser(strings.NewReader(string(item.body))), item.meta, nil
}

func (b *blobs) Delete(ctx context.Context, key string) error {
	if err := b.gate(true); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.items, key)
	return nil
}

func (b *blobs) URL(key string) string {
	return "/api/plugins/" + b.id + "/blobs/" + key
}

var _ hoststorage.DB = (*db)(nil)
var _ hoststorage.Tx = (*tx)(nil)
var _ hoststorage.Blobs = (*blobs)(nil)
var _ = hostpolicy.ErrPluginDisabled
