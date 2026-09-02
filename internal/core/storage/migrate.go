package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Each core module owns a migration namespace and applies its own migrations. Tables are
// prefixed by owner: `core_` for core modules, `<plugin_id>_` for a plugin.
const migrationTable = `
CREATE TABLE IF NOT EXISTS core_migrations (
    namespace  TEXT    NOT NULL,
    version    INTEGER NOT NULL,
    name       TEXT    NOT NULL,
    checksum   TEXT    NOT NULL,
    applied_at TEXT    NOT NULL,
    PRIMARY KEY (namespace, version)
) STRICT;`

// storageMigrations are owned by this module.
var storageMigrations = []Migration{
	{Version: 1, Name: "blobs", Up: `
CREATE TABLE core_blobs (
    scope         TEXT    NOT NULL,
    key           TEXT    NOT NULL,
    physical_name TEXT    NOT NULL,
    mime          TEXT    NOT NULL,
    size          INTEGER NOT NULL,
    sha256        TEXT    NOT NULL,
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL,
    PRIMARY KEY (scope, key)
) STRICT;

CREATE UNIQUE INDEX core_blobs_physical ON core_blobs(physical_name);
`},
}

// Apply runs a namespace's migrations idempotently, in order, recording what ran.
//
// Versions must be positive and strictly increasing. An already-applied version whose
// name or SQL differs, a duplicate version, or a gap below the highest applied version
// is a startup failure for that owner. A failed migration rolls back completely and is
// not recorded.
func (s *Store) Apply(namespace string, migrations []Migration) error {
	if namespace == "" {
		return errors.New("storage: empty migration namespace")
	}
	s.migrateMu.Lock()
	defer s.migrateMu.Unlock()

	ctx := context.Background()
	if _, err := s.Exec(ctx, migrationTable); err != nil {
		return fmt.Errorf("storage: create migration table: %w", err)
	}

	if err := validateOrder(namespace, migrations); err != nil {
		return err
	}

	applied, err := s.appliedMigrations(ctx, namespace)
	if err != nil {
		return err
	}

	highestApplied := 0
	for v := range applied {
		if v > highestApplied {
			highestApplied = v
		}
	}

	seen := 0
	for _, m := range migrations {
		sum := checksum(m)
		if prev, ok := applied[m.Version]; ok {
			if prev.name != m.Name || prev.checksum != sum {
				return fmt.Errorf(
					"storage: migration %s/%d already applied as %q (%s) but declared as %q (%s)",
					namespace, m.Version, prev.name, prev.checksum[:12], m.Name, sum[:12])
			}
			seen++
			continue
		}
		if m.Version < highestApplied {
			return fmt.Errorf(
				"storage: migration %s/%d is unapplied but version %d already ran; migrations may not be inserted below the high-water mark",
				namespace, m.Version, highestApplied)
		}
		if err := s.applyOne(ctx, namespace, m, sum); err != nil {
			return err
		}
	}
	if seen != len(applied) {
		return fmt.Errorf(
			"storage: namespace %s has %d applied migrations but only %d are declared; a previously applied migration was removed",
			namespace, len(applied), seen)
	}
	return nil
}

func (s *Store) applyPrefixed(namespace, prefix string, migrations []Migration) error {
	if namespace == "" {
		return errors.New("storage: empty migration namespace")
	}
	s.migrateMu.Lock()
	defer s.migrateMu.Unlock()

	ctx := context.Background()
	if _, err := s.Exec(ctx, migrationTable); err != nil {
		return fmt.Errorf("storage: create migration table: %w", err)
	}
	if err := validateOrder(namespace, migrations); err != nil {
		return err
	}
	applied, err := s.appliedMigrations(ctx, namespace)
	if err != nil {
		return err
	}
	highestApplied := 0
	for v := range applied {
		if v > highestApplied {
			highestApplied = v
		}
	}
	seen := 0
	for _, m := range migrations {
		sum := checksum(m)
		if prev, ok := applied[m.Version]; ok {
			if prev.name != m.Name || prev.checksum != sum {
				return fmt.Errorf(
					"storage: migration %s/%d already applied as %q (%s) but declared as %q (%s)",
					namespace, m.Version, prev.name, prev.checksum[:12], m.Name, sum[:12])
			}
			seen++
			continue
		}
		if m.Version < highestApplied {
			return fmt.Errorf(
				"storage: migration %s/%d is unapplied but version %d already ran; migrations may not be inserted below the high-water mark",
				namespace, m.Version, highestApplied)
		}
		if err := s.applyOnePrefixed(ctx, namespace, prefix, m, sum); err != nil {
			return err
		}
	}
	if seen != len(applied) {
		return fmt.Errorf(
			"storage: namespace %s has %d applied migrations but only %d are declared; a previously applied migration was removed",
			namespace, len(applied), seen)
	}
	return nil
}

func (s *Store) applyOnePrefixed(ctx context.Context, namespace, prefix string, m Migration, sum string) error {
	return s.Tx(ctx, func(tx Tx) error {
		// The authorizer sees the table an ALTER TABLE renames, never the name it is
		// renamed to, so `ALTER TABLE plug_x RENAME TO core_x` looks like a legal
		// operation on a legal table. Comparing the schema either side of the migration
		// is what closes that: the authorizer governs what the SQL may touch, and this
		// governs what the database is left holding.
		before, err := schemaOutside(ctx, tx, prefix)
		if err != nil {
			return err
		}
		if err := s.SetPluginAuthorizer(prefix); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, m.Up); err != nil {
			_ = s.SetPluginAuthorizer("")
			return wrapSQLDenied(fmt.Errorf("storage: migration %s/%d %q: %w", namespace, m.Version, m.Name, err))
		}
		if err := s.SetPluginAuthorizer(""); err != nil {
			return err
		}
		after, err := schemaOutside(ctx, tx, prefix)
		if err != nil {
			return err
		}
		for name := range after {
			if _, existed := before[name]; !existed {
				return fmt.Errorf(
					"%w: migration %s/%d %q created %q, which is outside the %q namespace",
					ErrSQLDenied, namespace, m.Version, m.Name, name, prefix)
			}
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO core_migrations(namespace, version, name, checksum, applied_at)
			 VALUES (?, ?, ?, ?, ?)`,
			namespace, m.Version, m.Name, sum, time.Now().UTC().Format(time.RFC3339Nano))
		return err
	})
}

func (s *Store) applyOne(ctx context.Context, namespace string, m Migration, sum string) error {
	err := s.Tx(ctx, func(tx Tx) error {
		if _, err := tx.Exec(ctx, m.Up); err != nil {
			return fmt.Errorf("storage: migration %s/%d %q: %w", namespace, m.Version, m.Name, err)
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO core_migrations(namespace, version, name, checksum, applied_at)
			 VALUES (?, ?, ?, ?, ?)`,
			namespace, m.Version, m.Name, sum, time.Now().UTC().Format(time.RFC3339Nano))
		return err
	})
	return err
}

type appliedMigration struct {
	name     string
	checksum string
}

func (s *Store) appliedMigrations(ctx context.Context, namespace string) (map[int]appliedMigration, error) {
	rows, err := s.Query(ctx,
		`SELECT version, name, checksum FROM core_migrations WHERE namespace = ?`, namespace)
	if err != nil {
		return nil, fmt.Errorf("storage: read migrations: %w", err)
	}
	defer rows.Close()

	out := make(map[int]appliedMigration)
	for rows.Next() {
		var v int
		var a appliedMigration
		if err := rows.Scan(&v, &a.name, &a.checksum); err != nil {
			return nil, err
		}
		out[v] = a
	}
	return out, rows.Err()
}

func validateOrder(namespace string, migrations []Migration) error {
	prev := 0
	for _, m := range migrations {
		if m.Version <= 0 {
			return fmt.Errorf("storage: migration %s/%d: version must be positive", namespace, m.Version)
		}
		if m.Version <= prev {
			return fmt.Errorf("storage: migration %s/%d: versions must strictly increase (previous was %d)",
				namespace, m.Version, prev)
		}
		if m.Name == "" {
			return fmt.Errorf("storage: migration %s/%d: empty name", namespace, m.Version)
		}
		prev = m.Version
	}
	return nil
}

func checksum(m Migration) string {
	h := sha256.New()
	h.Write([]byte(m.Name))
	h.Write([]byte{0})
	h.Write([]byte(m.Up))
	return hex.EncodeToString(h.Sum(nil))
}

// IsNoRows reports whether err is sql.ErrNoRows, so callers need not import database/sql
// just to branch on an empty read.
func IsNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// schemaOutside names every schema object that does not belong to prefix. Objects
// SQLite owns are skipped: they are not the plugin's to create and cannot be named by
// one, since every identifier a plugin declares must carry its prefix.
func schemaOutside(ctx context.Context, tx Tx, prefix string) (map[string]struct{}, error) {
	rows, err := tx.Query(ctx, `SELECT name FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, fmt.Errorf("storage: read schema: %w", err)
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(name, prefix) {
			out[name] = struct{}{}
		}
	}
	return out, rows.Err()
}
