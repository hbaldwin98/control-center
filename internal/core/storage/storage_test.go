package storage

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), Options{Path: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpenAppliesOwnMigrations(t *testing.T) {
	s := openTest(t)

	var n int
	err := s.QueryRow(context.Background(),
		`SELECT count(*) FROM core_migrations WHERE namespace = 'storage'`).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(storageMigrations) {
		t.Fatalf("applied %d migrations, want %d", n, len(storageMigrations))
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	s := openTest(t)
	mig := []Migration{{Version: 1, Name: "t", Up: `CREATE TABLE demo_a(x INTEGER) STRICT;`}}

	for i := range 3 {
		if err := s.Apply("demo", mig); err != nil {
			t.Fatalf("apply %d: %v", i, err)
		}
	}
}

func TestApplyRejectsChangedMigration(t *testing.T) {
	s := openTest(t)
	if err := s.Apply("demo", []Migration{
		{Version: 1, Name: "t", Up: `CREATE TABLE demo_a(x INTEGER) STRICT;`},
	}); err != nil {
		t.Fatal(err)
	}
	err := s.Apply("demo", []Migration{
		{Version: 1, Name: "t", Up: `CREATE TABLE demo_a(x TEXT) STRICT;`},
	})
	if err == nil {
		t.Fatal("expected a checksum mismatch failure")
	}
}

func TestApplyRejectsInsertedVersion(t *testing.T) {
	s := openTest(t)
	if err := s.Apply("demo", []Migration{
		{Version: 5, Name: "five", Up: `CREATE TABLE demo_a(x INTEGER) STRICT;`},
	}); err != nil {
		t.Fatal(err)
	}
	err := s.Apply("demo", []Migration{
		{Version: 3, Name: "three", Up: `CREATE TABLE demo_b(x INTEGER) STRICT;`},
		{Version: 5, Name: "five", Up: `CREATE TABLE demo_a(x INTEGER) STRICT;`},
	})
	if err == nil {
		t.Fatal("expected a below-high-water-mark failure")
	}
}

func TestApplyRejectsRemovedMigration(t *testing.T) {
	s := openTest(t)
	if err := s.Apply("demo", []Migration{
		{Version: 1, Name: "one", Up: `CREATE TABLE demo_a(x INTEGER) STRICT;`},
		{Version: 2, Name: "two", Up: `CREATE TABLE demo_b(x INTEGER) STRICT;`},
	}); err != nil {
		t.Fatal(err)
	}
	err := s.Apply("demo", []Migration{
		{Version: 2, Name: "two", Up: `CREATE TABLE demo_b(x INTEGER) STRICT;`},
	})
	if err == nil {
		t.Fatal("expected a removed-migration failure")
	}
}

func TestFailedMigrationIsNotRecorded(t *testing.T) {
	s := openTest(t)
	err := s.Apply("demo", []Migration{{Version: 1, Name: "bad", Up: `NOT SQL AT ALL;`}})
	if err == nil {
		t.Fatal("expected the bad migration to fail")
	}
	var n int
	if err := s.QueryRow(context.Background(),
		`SELECT count(*) FROM core_migrations WHERE namespace = 'demo'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("recorded %d rows for a failed migration, want 0", n)
	}
}

func TestTxRollsBackOnError(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if err := s.Apply("demo", []Migration{
		{Version: 1, Name: "t", Up: `CREATE TABLE demo_a(x INTEGER) STRICT;`},
	}); err != nil {
		t.Fatal(err)
	}

	sentinel := errors.New("nope")
	err := s.Tx(ctx, func(tx Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO demo_a(x) VALUES (1)`); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("got %v, want the sentinel", err)
	}

	var n int
	if err := s.QueryRow(ctx, `SELECT count(*) FROM demo_a`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("rolled-back row survived: %d", n)
	}
}

func TestTxRollsBackOnPanic(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if err := s.Apply("demo", []Migration{
		{Version: 1, Name: "t", Up: `CREATE TABLE demo_a(x INTEGER) STRICT;`},
	}); err != nil {
		t.Fatal(err)
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Error("expected the panic to propagate")
			}
		}()
		_ = s.Tx(ctx, func(tx Tx) error {
			_, _ = tx.Exec(ctx, `INSERT INTO demo_a(x) VALUES (1)`)
			panic("boom")
		})
	}()

	var n int
	if err := s.QueryRow(ctx, `SELECT count(*) FROM demo_a`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("row survived a panicking transaction: %d", n)
	}
	// The writer must be usable again.
	if err := s.Tx(ctx, func(Tx) error { return nil }); err != nil {
		t.Fatalf("writer wedged after panic: %v", err)
	}
}

func TestNestedTxFailsFast(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	err := s.Tx(ctx, func(Tx) error {
		return s.Tx(ctx, func(Tx) error { return nil })
	})
	if !errors.Is(err, ErrNestedTx) {
		t.Fatalf("got %v, want ErrNestedTx", err)
	}
}

func TestTxHonoursCancellationWhileWaiting(t *testing.T) {
	s := openTest(t)

	held := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = s.Tx(context.Background(), func(Tx) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := s.Tx(ctx, func(Tx) error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want DeadlineExceeded", err)
	}
}

func TestAfterCommitRunsAfterCommitAndNotOnRollback(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if err := s.Apply("demo", []Migration{
		{Version: 1, Name: "t", Up: `CREATE TABLE demo_a(x INTEGER) STRICT;`},
	}); err != nil {
		t.Fatal(err)
	}

	var committed, rolledBack bool
	if err := s.Tx(ctx, func(tx Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO demo_a(x) VALUES (1)`); err != nil {
			return err
		}
		tx.AfterCommit(func() { committed = true })
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !committed {
		t.Fatal("AfterCommit did not run after a successful commit")
	}

	sentinel := errors.New("nope")
	if err := s.Tx(ctx, func(tx Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO demo_a(x) VALUES (2)`); err != nil {
			return err
		}
		tx.AfterCommit(func() { rolledBack = true })
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("got %v, want the sentinel", err)
	}
	if rolledBack {
		t.Fatal("AfterCommit ran for a rolled-back transaction")
	}

	var n int
	if err := s.QueryRow(ctx, `SELECT count(*) FROM demo_a`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rows = %d, want the committed insert only", n)
	}
}

func TestAfterCommitMayOpenANewTransaction(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if err := s.Apply("demo", []Migration{
		{Version: 1, Name: "t", Up: `CREATE TABLE demo_a(x INTEGER) STRICT;`},
	}); err != nil {
		t.Fatal(err)
	}

	// AfterCommit runs after the write lock is released. A watcher that opens a new
	// transaction must not deadlock on the same goroutine.
	if err := s.Tx(ctx, func(tx Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO demo_a(x) VALUES (1)`); err != nil {
			return err
		}
		tx.AfterCommit(func() {
			if err := s.Tx(ctx, func(tx Tx) error {
				_, err := tx.Exec(ctx, `INSERT INTO demo_a(x) VALUES (2)`)
				return err
			}); err != nil {
				t.Errorf("nested AfterCommit Tx: %v", err)
			}
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := s.QueryRow(ctx, `SELECT count(*) FROM demo_a`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("rows = %d, want both the original and the AfterCommit insert", n)
	}
}

func TestConcurrentWritersSerialize(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if err := s.Apply("demo", []Migration{
		{Version: 1, Name: "t", Up: `CREATE TABLE demo_a(x INTEGER) STRICT;`},
	}); err != nil {
		t.Fatal(err)
	}

	const writers = 16
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := range writers {
		go func(i int) {
			defer wg.Done()
			if err := s.Tx(ctx, func(tx Tx) error {
				_, err := tx.Exec(ctx, `INSERT INTO demo_a(x) VALUES (?)`, i)
				return err
			}); err != nil {
				t.Errorf("writer %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	var n int
	if err := s.QueryRow(ctx, `SELECT count(*) FROM demo_a`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != writers {
		t.Fatalf("wrote %d rows, want %d", n, writers)
	}
}
