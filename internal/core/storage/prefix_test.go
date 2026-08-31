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
