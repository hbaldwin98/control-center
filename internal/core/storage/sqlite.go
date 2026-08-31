package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Options configures the SQLite store.
type Options struct {
	// Path is the main database file. Its directory is created if missing.
	Path string

	// BusyTimeout must be finite. SQLite waits at most this long for a lock before
	// returning SQLITE_BUSY, which surfaces as an error rather than an unbounded stall.
	BusyTimeout time.Duration

	// ReadPool bounds concurrent readers. WAL lets them run alongside the writer.
	ReadPool int
}

func (o *Options) applyDefaults() {
	if o.BusyTimeout <= 0 {
		o.BusyTimeout = 5 * time.Second
	}
	if o.ReadPool <= 0 {
		o.ReadPool = 8
	}
}

// Store is the concrete SQLite implementation of DB and Migrator.
//
// Writes go through a single serialized connection so write ordering is explicit and
// SQLITE_BUSY cannot arise between our own transactions. Reads use a bounded pool.
type Store struct {
	writer *sql.DB
	reader *sql.DB
	path   string

	// writeSem serializes transactions. It is a channel rather than a mutex so that
	// acquisition can honour context cancellation.
	writeSem chan struct{}

	// txOwner records the goroutine currently inside a transaction callback, so an
	// illegal nested DB.Tx fails fast instead of deadlocking on writeSem.
	txOwnerMu sync.Mutex
	txOwner   map[uint64]struct{}

	migrateMu sync.Mutex

	// currentWriter is the checked-out writer connection during Tx. pluginhost sets a
	// SQLite authorizer on it for plugin SQL, then clears it before writing core rows.
	currentWriter *sql.Conn
}

var _ DB = (*Store)(nil)
var _ Migrator = (*Store)(nil)

// Open prepares the database directory, opens the writer and reader pools, and applies
// the core migrations.
func Open(ctx context.Context, opts Options) (*Store, error) {
	opts.applyDefaults()
	if opts.Path == "" {
		return nil, errors.New("storage: empty database path")
	}
	if err := os.MkdirAll(filepath.Dir(opts.Path), 0o700); err != nil {
		return nil, fmt.Errorf("storage: create data dir: %w", err)
	}

	writer, err := openConn(opts, true)
	if err != nil {
		return nil, err
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	writer.SetConnMaxLifetime(0)

	reader, err := openConn(opts, false)
	if err != nil {
		writer.Close()
		return nil, err
	}
	reader.SetMaxOpenConns(opts.ReadPool)
	reader.SetMaxIdleConns(opts.ReadPool)
	reader.SetConnMaxLifetime(0)

	s := &Store{
		writer:   writer,
		reader:   reader,
		path:     opts.Path,
		writeSem: make(chan struct{}, 1),
		txOwner:  make(map[uint64]struct{}),
	}

	if err := writer.PingContext(ctx); err != nil {
		s.Close()
		return nil, fmt.Errorf("storage: ping: %w", err)
	}
	if err := s.Apply("storage", storageMigrations); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func openConn(opts Options, writer bool) (*sql.DB, error) {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout("+strconv.FormatInt(opts.BusyTimeout.Milliseconds(), 10)+")")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(ON)")
	if writer {
		// A short WAL autocheckpoint keeps the WAL from growing without bound between
		// the coordinated checkpoints taken by the backup path.
		q.Add("_pragma", "wal_autocheckpoint(1000)")
	}
	dsn := "file:" + filepath.ToSlash(opts.Path) + "?" + q.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("storage: open %s: %w", opts.Path, err)
	}
	return db, nil
}

// Path returns the main database file path.
func (s *Store) Path() string { return s.path }

// Close releases both connection pools.
func (s *Store) Close() error {
	var first error
	if s.reader != nil {
		if err := s.reader.Close(); err != nil {
			first = err
		}
	}
	if s.writer != nil {
		if err := s.writer.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Query runs a read on the reader pool.
func (s *Store) Query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return s.reader.QueryContext(ctx, q, args...)
}

// QueryRow runs a single-row read on the reader pool.
func (s *Store) QueryRow(ctx context.Context, q string, args ...any) *sql.Row {
	return s.reader.QueryRowContext(ctx, q, args...)
}

// Exec runs one statement as its own implicit transaction on the serialized writer.
func (s *Store) Exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	if err := s.acquire(ctx); err != nil {
		return nil, err
	}
	defer s.release()
	return s.writer.ExecContext(ctx, q, args...)
}

// Tx runs fn inside one write transaction. It commits only when fn returns nil; errors,
// context cancellation, and panics roll it back.
//
// A transaction callback may not call Tx or Exec again: the writer is already held.
// AfterCommit callbacks run after the commit and after the write lock is released, so
// they may open a new transaction without deadlocking.
func (s *Store) Tx(ctx context.Context, fn func(Tx) error) (err error) {
	if err := s.acquire(ctx); err != nil {
		return err
	}

	conn, err := s.writer.Conn(ctx)
	if err != nil {
		s.release()
		return fmt.Errorf("storage: writer conn: %w", err)
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		_ = conn.Close()
		s.release()
		return fmt.Errorf("storage: begin: %w", err)
	}

	s.currentWriter = conn
	handle := &sqlTx{tx: tx}
	committed := false
	defer func() {
		s.currentWriter = nil
		if p := recover(); p != nil {
			_ = tx.Rollback()
			_ = conn.Close()
			s.release()
			panic(p)
		}
		if !committed {
			_ = tx.Rollback()
			_ = conn.Close()
			s.release()
		}
	}()

	if err := fn(handle); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit: %w", err)
	}
	committed = true
	_ = conn.Close()
	s.release()
	for _, fn := range handle.after {
		fn()
	}
	return nil
}

// acquire takes the serialized writer, failing fast on illegal nesting and returning the
// context error if the caller is cancelled while waiting.
func (s *Store) acquire(ctx context.Context) error {
	gid := goroutineID()
	s.txOwnerMu.Lock()
	_, nested := s.txOwner[gid]
	s.txOwnerMu.Unlock()
	if nested {
		return ErrNestedTx
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.writeSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.txOwnerMu.Lock()
	s.txOwner[gid] = struct{}{}
	s.txOwnerMu.Unlock()
	return nil
}

func (s *Store) release() {
	gid := goroutineID()
	s.txOwnerMu.Lock()
	delete(s.txOwner, gid)
	s.txOwnerMu.Unlock()
	<-s.writeSem
}

type sqlTx struct {
	tx    *sql.Tx
	after []func()
}

func (t *sqlTx) Query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return t.tx.QueryContext(ctx, q, args...)
}

func (t *sqlTx) QueryRow(ctx context.Context, q string, args ...any) *sql.Row {
	return t.tx.QueryRowContext(ctx, q, args...)
}

func (t *sqlTx) Exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(ctx, q, args...)
}

func (t *sqlTx) AfterCommit(fn func()) {
	if fn != nil {
		t.after = append(t.after, fn)
	}
}

// goroutineID parses the current goroutine's ID from its stack header. Go offers no
// supported accessor; this is used only to convert an illegal nested transaction from a
// deadlock into ErrNestedTx.
func goroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	// "goroutine 123 [running]:"
	b := buf[:n]
	const prefix = "goroutine "
	if len(b) < len(prefix) {
		return 0
	}
	b = b[len(prefix):]
	end := 0
	for end < len(b) && b[end] >= '0' && b[end] <= '9' {
		end++
	}
	id, err := strconv.ParseUint(string(b[:end]), 10, 64)
	if err != nil {
		return 0
	}
	return id
}
