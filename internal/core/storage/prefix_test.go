package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestPrefixedAuthorizerAllowsOnlyPluginTables(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "prefix.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	m := store.PluginMigrator("probe")
	if err := m.Apply("probe", []Migration{{Version: 1, Name: "ticks", Up: `
		CREATE TABLE probe_ticks (
			id INTEGER PRIMARY KEY,
			note TEXT NOT NULL
		) STRICT;
	`}}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	p := Prefixed(store, "probe")
	if _, err := p.Exec(ctx, `INSERT INTO probe_ticks(note) VALUES ('ok')`); err != nil {
		t.Fatalf("plugin insert: %v", err)
	}
	var note string
	if err := p.QueryRow(ctx, `SELECT note FROM probe_ticks`).Scan(&note); err != nil {
		t.Fatalf("plugin select: %v", err)
	}
	if note != "ok" {
		t.Fatalf("note = %q", note)
	}

	if _, err := p.Exec(ctx, `DELETE FROM core_blobs`); err == nil || !errors.Is(err, ErrSQLDenied) {
		t.Fatalf("expected denied core delete, got %v", err)
	}
	if _, err := p.Query(ctx, `SELECT key FROM core_blobs`); err == nil {
		t.Fatal("expected denied core select")
	}
}

func TestPrefixedMigrationCannotCreateCoreTable(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "prefix.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	err = store.PluginMigrator("probe").Apply("probe", []Migration{{
		Version: 1, Name: "evil", Up: `CREATE TABLE core_evil (id INTEGER PRIMARY KEY);`,
	}})
	if err == nil {
		t.Fatal("expected denied core table create")
	}
	if !errors.Is(err, ErrSQLDenied) {
		t.Fatalf("got %v", err)
	}
}

func TestPrefixedMigrationCanAlterPluginTable(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "prefix.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	m := store.PluginMigrator("probe")
	if err := m.Apply("probe", []Migration{
		{Version: 1, Name: "ticks", Up: `CREATE TABLE probe_ticks (id INTEGER PRIMARY KEY, note TEXT NOT NULL) STRICT;
			CREATE TABLE probe_lots (id TEXT PRIMARY KEY, title TEXT NOT NULL) STRICT;
			CREATE TABLE probe_analyses (id INTEGER PRIMARY KEY, notes TEXT NOT NULL) STRICT;`},
		{Version: 2, Name: "ends", Up: `
			ALTER TABLE probe_ticks ADD COLUMN ends_at TEXT NOT NULL DEFAULT '';
			ALTER TABLE probe_lots ADD COLUMN ends_at TEXT NOT NULL DEFAULT '';
			ALTER TABLE probe_lots ADD COLUMN bid_count INTEGER NOT NULL DEFAULT 0;
			ALTER TABLE probe_lots ADD COLUMN min_bid_cents INTEGER;
			ALTER TABLE probe_analyses ADD COLUMN search_terms TEXT NOT NULL DEFAULT '[]';
		`},
	}); err != nil {
		t.Fatalf("alter plugin table: %v", err)
	}
	p := Prefixed(store, "probe")
	if _, err := p.Exec(ctx, `INSERT INTO probe_ticks(note, ends_at) VALUES ('ok', 'soon')`); err != nil {
		t.Fatalf("insert after alter: %v", err)
	}
	if err := m.Apply("probe", []Migration{
		{Version: 3, Name: "evil", Up: `ALTER TABLE core_blobs ADD COLUMN nope TEXT NOT NULL DEFAULT '';`},
	}); err == nil || !errors.Is(err, ErrSQLDenied) {
		t.Fatalf("expected denied core alter, got %v", err)
	}
}
