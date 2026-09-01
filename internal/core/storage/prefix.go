package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// Prefixed returns the SQL handle a plugin sees. Runtime SQL and plugin migrations
// run with a SQLite authorizer that allows only names beginning with pluginID+"_".
func Prefixed(db DB, pluginID string) *PrefixDB {
	s, ok := db.(*Store)
	if !ok {
		panic("storage: Prefixed requires *Store")
	}
	return &PrefixDB{s: s, pluginID: pluginID, prefix: pluginID + "_"}
}

// PrefixDB is the plugin-scoped SQL surface. pluginhost gates mutations separately.
type PrefixDB struct {
	s        *Store
	pluginID string
	prefix   string
}

// ResultSet is a prefix-authorized result. Close returns the reader connection.
type ResultSet struct {
	rows *sql.Rows
	conn *sql.Conn
}

func (r *ResultSet) Next() bool                  { return r.rows.Next() }
func (r *ResultSet) Scan(dest ...any) error      { return r.rows.Scan(dest...) }
func (r *ResultSet) Err() error                  { return r.rows.Err() }
func (r *ResultSet) Columns() ([]string, error)  { return r.rows.Columns() }
func (r *ResultSet) Close() error {
	err := r.rows.Close()
	cerr := r.conn.Close()
	if err != nil {
		return err
	}
	return cerr
}

// SingleRow is a prefix-authorized single-row result. Scan releases the connection.
type SingleRow struct {
	rs  *ResultSet
	err error
}

func (r *SingleRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	defer r.rs.Close()
	if !r.rs.Next() {
		if err := r.rs.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	if err := r.rs.Scan(dest...); err != nil {
		return err
	}
	return r.rs.Err()
}

func (p *PrefixDB) Query(ctx context.Context, q string, args ...any) (*ResultSet, error) {
	conn, err := p.s.reader.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if err := setConnAuthorizer(conn, p.prefix); err != nil {
		_ = conn.Close()
		return nil, err
	}
	rows, err := conn.QueryContext(ctx, q, args...)
	_ = setConnAuthorizer(conn, "")
	if err != nil {
		_ = conn.Close()
		return nil, wrapSQLDenied(err)
	}
	return &ResultSet{rows: rows, conn: conn}, nil
}

func (p *PrefixDB) QueryRow(ctx context.Context, q string, args ...any) *SingleRow {
	rs, err := p.Query(ctx, q, args...)
	if err != nil {
		return &SingleRow{err: err}
	}
	return &SingleRow{rs: rs}
}

func (p *PrefixDB) Exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	var res sql.Result
	err := p.s.Tx(ctx, func(tx Tx) error {
		if err := p.s.SetPluginAuthorizer(p.prefix); err != nil {
			return err
		}
		defer func() { _ = p.s.SetPluginAuthorizer("") }()
		var err error
		res, err = tx.Exec(ctx, q, args...)
		return wrapSQLDenied(err)
	})
	return res, err
}

func (p *PrefixDB) Tx(ctx context.Context, fn func(Tx) error) error {
	return p.s.Tx(ctx, func(tx Tx) error {
		return fn(&prefixTx{s: p.s, tx: tx, prefix: p.prefix})
	})
}

type prefixTx struct {
	s      *Store
	tx     Tx
	prefix string
}

func (t *prefixTx) Query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	if err := t.s.SetPluginAuthorizer(t.prefix); err != nil {
		return nil, err
	}
	defer func() { _ = t.s.SetPluginAuthorizer("") }()
	rows, err := t.tx.Query(ctx, q, args...)
	return rows, wrapSQLDenied(err)
}

func (t *prefixTx) QueryRow(ctx context.Context, q string, args ...any) *sql.Row {
	_ = t.s.SetPluginAuthorizer(t.prefix)
	defer func() { _ = t.s.SetPluginAuthorizer("") }()
	return t.tx.QueryRow(ctx, q, args...)
}

func (t *prefixTx) Exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	if err := t.s.SetPluginAuthorizer(t.prefix); err != nil {
		return nil, err
	}
	defer func() { _ = t.s.SetPluginAuthorizer("") }()
	res, err := t.tx.Exec(ctx, q, args...)
	return res, wrapSQLDenied(err)
}

func (t *prefixTx) AfterCommit(fn func()) { t.tx.AfterCommit(fn) }

// PluginMigrator applies migrations under pluginID's namespace with the prefix authorizer.
func (s *Store) PluginMigrator(pluginID string) Migrator {
	return pluginMigrator{s: s, pluginID: pluginID, prefix: pluginID + "_"}
}

type pluginMigrator struct {
	s        *Store
	pluginID string
	prefix   string
}

func (m pluginMigrator) Apply(namespace string, migrations []Migration) error {
	if namespace == "" {
		namespace = m.pluginID
	}
	if namespace != m.pluginID {
		return fmt.Errorf("storage: plugin migrator namespace %q does not match %q", namespace, m.pluginID)
	}
	return m.s.applyPrefixed(m.pluginID, m.prefix, migrations)
}
