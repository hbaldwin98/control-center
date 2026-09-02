package storage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// ALTER TABLE ... RENAME inside a plugin migration.
//
// This failed in production while passing every plugin's own tests, which is the
// interesting part. SQLite implements the rename by scanning `sqlite_sequence` for the
// renamed table's AUTOINCREMENT counter -- it does that whether or not the table has
// one. `sqlite_sequence` exists only once something in the database uses AUTOINCREMENT,
// and core does (events, jobs, ai, credentials). A plugin's test database holds only
// that plugin's tables, so the table was absent, nothing scanned it, and the migration
// passed. On a real database it is always there, and the read was denied.
//
// So the regression test has to create a core AUTOINCREMENT table first. Without that
// line it passes against the unfixed authorizer.
func TestPluginRenameWithCoreSequenceTable(t *testing.T) {
	st := openTestStore(t)

	// Core owns an AUTOINCREMENT table, so sqlite_sequence exists. This is the line the
	// original reproduction was missing.
	if _, err := st.Exec(context.Background(),
		`CREATE TABLE core_things (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Exec(context.Background(), `INSERT INTO core_things(name) VALUES ('one')`); err != nil {
		t.Fatal(err)
	}

	steps := []Migration{
		{Version: 1, Name: "create", Up: `
			CREATE TABLE plug_rows (
				period_start TEXT NOT NULL,
				period_end TEXT NOT NULL,
				kwh REAL NOT NULL,
				PRIMARY KEY(period_start, period_end)
			) STRICT;
			CREATE INDEX plug_rows_start ON plug_rows(period_start);
		`},
		{Version: 2, Name: "normalize", Up: `
			CREATE TABLE plug_rows_normalized (
				period_start TEXT NOT NULL,
				period_end TEXT NOT NULL,
				kwh REAL NOT NULL,
				PRIMARY KEY(period_start, period_end)
			) STRICT;
			INSERT INTO plug_rows_normalized
			SELECT substr(period_start, 1, 10), substr(period_end, 1, 10), MAX(kwh)
			  FROM plug_rows
			 GROUP BY substr(period_start, 1, 10), substr(period_end, 1, 10);
			DROP TABLE plug_rows;
			ALTER TABLE plug_rows_normalized RENAME TO plug_rows;
			CREATE INDEX plug_rows_start ON plug_rows(period_start);
		`},
	}
	if err := st.applyPrefixed("plug", "plug_", steps); err != nil {
		t.Fatalf("plugin rename migration: %v", err)
	}

	// The renamed table is usable afterwards, under its new name.
	if _, err := st.Exec(context.Background(),
		`INSERT INTO plug_rows(period_start, period_end, kwh) VALUES ('2026-01-01', '2026-01-31', 4.5)`); err != nil {
		t.Fatalf("insert after rename: %v", err)
	}
}

// Reading sqlite_sequence is allowed because DDL needs it. Writing it is not: the
// authorizer is told only the table name, never which counter a write would touch, so
// there is no way to confine a write to the plugin's own tables.
func TestPluginCannotWriteTheSequenceTable(t *testing.T) {
	st := openTestStore(t)
	if _, err := st.Exec(context.Background(),
		`CREATE TABLE core_things (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Exec(context.Background(), `INSERT INTO core_things(name) VALUES ('one')`); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		sql  string
	}{
		{"update another table's counter", `UPDATE sqlite_sequence SET seq = 0 WHERE name = 'core_things'`},
		{"delete a counter", `DELETE FROM sqlite_sequence WHERE name = 'core_things'`},
		{"insert a counter", `INSERT INTO sqlite_sequence(name, seq) VALUES ('core_things', 9999)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := st.applyPrefixed("plug"+tc.name, "plug_", []Migration{
				{Version: 1, Name: "tamper", Up: tc.sql},
			})
			if err == nil {
				t.Fatal("a plugin must not be able to write sqlite_sequence")
			}
			if !strings.Contains(err.Error(), "outside the plugin namespace") {
				t.Fatalf("err = %v, want a namespace denial", err)
			}
		})
	}

	// The core counter is untouched.
	var seq int
	if err := st.QueryRow(context.Background(),
		`SELECT seq FROM sqlite_sequence WHERE name = 'core_things'`).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if seq != 1 {
		t.Fatalf("core counter = %d, want 1", seq)
	}
}

// Reading it is allowed, but only as a side effect of SQLite's own DDL: a plugin still
// cannot reach another plugin's or core's tables.
func TestPluginNamespaceStillHolds(t *testing.T) {
	st := openTestStore(t)
	if _, err := st.Exec(context.Background(),
		`CREATE TABLE core_secrets (id INTEGER PRIMARY KEY AUTOINCREMENT, token TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		sql  string
	}{
		{"select from a core table", `CREATE TABLE plug_copy AS SELECT * FROM core_secrets`},
		{"drop a core table", `DROP TABLE core_secrets`},
		{"rename a core table", `ALTER TABLE core_secrets RENAME TO plug_secrets`},
		{"rename a plugin table onto a core name", `CREATE TABLE plug_x (a TEXT) STRICT; ALTER TABLE plug_x RENAME TO core_x`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := st.applyPrefixed("plug"+tc.name, "plug_", []Migration{
				{Version: 1, Name: "reach", Up: tc.sql},
			})
			if err == nil {
				t.Fatalf("plugin SQL %q was allowed", tc.sql)
			}
		})
	}
}

func TestIsSequenceTable(t *testing.T) {
	allowed := []string{"sqlite_sequence", "SQLITE_SEQUENCE", "main.sqlite_sequence", ".sqlite_sequence"}
	for _, name := range allowed {
		if !isSequenceTable(name) {
			t.Errorf("isSequenceTable(%q) = false, want true", name)
		}
	}
	denied := []string{"", "sqlite_sequences", "temp.sqlite_sequence", "other.sqlite_sequence", "plug_sequence", "sqlite_master"}
	for _, name := range denied {
		if isSequenceTable(name) {
			t.Errorf("isSequenceTable(%q) = true, want false", name)
		}
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(context.Background(), Options{Path: filepath.Join(t.TempDir(), "cc.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}
