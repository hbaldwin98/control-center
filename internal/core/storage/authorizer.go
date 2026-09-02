package storage

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// cFuncPointer converts a function declaration to a C function pointer. Closures
// are undefined; the trampoline below is a package function.
func cFuncPointer[T any](f T) uintptr {
	return *(*uintptr)(unsafe.Pointer(&struct{ f T }{f}))
}

type authorizerIDs struct {
	mu     sync.Mutex
	next   uintptr
	byID   map[uintptr]string
	byConn map[uintptr]uintptr // sqlite db handle → authorizer id
}

var authorizers = authorizerIDs{byID: map[uintptr]string{}, byConn: map[uintptr]uintptr{}, next: 1}

func (a *authorizerIDs) alloc(prefix string) uintptr {
	a.mu.Lock()
	defer a.mu.Unlock()
	id := a.next
	a.next++
	a.byID[id] = prefix
	return id
}

func (a *authorizerIDs) bind(db, id uintptr) {
	a.mu.Lock()
	a.byConn[db] = id
	a.mu.Unlock()
}

func (a *authorizerIDs) drop(db uintptr) {
	a.mu.Lock()
	if id, ok := a.byConn[db]; ok {
		delete(a.byID, id)
		delete(a.byConn, db)
	}
	a.mu.Unlock()
}

func (a *authorizerIDs) prefix(id uintptr) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.byID[id]
}

// setConnAuthorizer installs (or clears, when prefix is empty) a SQLite authorizer
// that allows only objects in prefix. Identifiers are those SQLite parsed, not a
// scan of the SQL text.
func setConnAuthorizer(c *sql.Conn, prefix string) error {
	return c.Raw(func(dc any) error {
		tls, db, err := sqliteHandles(dc)
		if err != nil {
			return err
		}
		authorizers.drop(db)
		if prefix == "" {
			sqlite3.Xsqlite3_set_authorizer(tls, db, 0, 0)
			return nil
		}
		id := authorizers.alloc(prefix)
		authorizers.bind(db, id)
		rc := sqlite3.Xsqlite3_set_authorizer(tls, db, cFuncPointer(authorizerTrampoline), id)
		if rc != sqlite3.SQLITE_OK {
			authorizers.drop(db)
			return fmt.Errorf("storage: sqlite3_set_authorizer: %d", rc)
		}
		return nil
	})
}

func sqliteHandles(dc any) (*libc.TLS, uintptr, error) {
	v := reflect.ValueOf(dc)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	dbField := v.FieldByName("db")
	tlsField := v.FieldByName("tls")
	if !dbField.IsValid() || !tlsField.IsValid() {
		return nil, 0, fmt.Errorf("storage: unexpected sqlite conn %T", dc)
	}
	db := uintptr(dbField.Uint())
	tls := *(**libc.TLS)(unsafe.Pointer(tlsField.UnsafeAddr()))
	if tls == nil || db == 0 {
		return nil, 0, fmt.Errorf("storage: sqlite conn handles are unset")
	}
	return tls, db, nil
}

func authorizerTrampoline(tls *libc.TLS, pArg uintptr, action int32, a1, a2, a3, a4 uintptr) int32 {
	prefix := authorizers.prefix(pArg)
	if prefix == "" {
		return sqlite3.SQLITE_DENY
	}
	return authorize(prefix, action, cstring(tls, a1), cstring(tls, a2), cstring(tls, a3), cstring(tls, a4))
}

func cstring(_ *libc.TLS, p uintptr) string {
	if p == 0 {
		return ""
	}
	return libc.GoString(p)
}

func authorize(prefix string, action int32, arg1, arg2, arg3, arg4 string) int32 {
	switch action {
	case sqlite3.SQLITE_TRANSACTION, sqlite3.SQLITE_SELECT, sqlite3.SQLITE_SAVEPOINT, sqlite3.SQLITE_RECURSIVE:
		return sqlite3.SQLITE_OK
	case sqlite3.SQLITE_ATTACH, sqlite3.SQLITE_DETACH:
		return sqlite3.SQLITE_DENY
	case sqlite3.SQLITE_CREATE_TEMP_TABLE, sqlite3.SQLITE_CREATE_TEMP_INDEX,
		sqlite3.SQLITE_CREATE_TEMP_TRIGGER, sqlite3.SQLITE_CREATE_TEMP_VIEW,
		sqlite3.SQLITE_DROP_TEMP_TABLE, sqlite3.SQLITE_DROP_TEMP_INDEX,
		sqlite3.SQLITE_DROP_TEMP_TRIGGER, sqlite3.SQLITE_DROP_TEMP_VIEW:
		return sqlite3.SQLITE_DENY
	case sqlite3.SQLITE_PRAGMA:
		// ALTER TABLE ADD COLUMN on STRICT tables issues PRAGMA quick_check(table).
		if strings.EqualFold(arg1, "quick_check") && tableAllowed(prefix, arg2) {
			return sqlite3.SQLITE_OK
		}
		return sqlite3.SQLITE_DENY
	case sqlite3.SQLITE_FUNCTION:
		if strings.EqualFold(arg2, "load_extension") {
			return sqlite3.SQLITE_DENY
		}
		return sqlite3.SQLITE_OK
	case sqlite3.SQLITE_CREATE_VTABLE, sqlite3.SQLITE_DROP_VTABLE:
		return sqlite3.SQLITE_DENY
	case sqlite3.SQLITE_READ, sqlite3.SQLITE_INSERT, sqlite3.SQLITE_UPDATE, sqlite3.SQLITE_DELETE,
		sqlite3.SQLITE_CREATE_TABLE, sqlite3.SQLITE_DROP_TABLE,
		sqlite3.SQLITE_ANALYZE:
		if tableAllowed(prefix, arg1) {
			return sqlite3.SQLITE_OK
		}
		return sqlite3.SQLITE_DENY
	case sqlite3.SQLITE_ALTER_TABLE:
		// arg1 is the database name, arg2 is the table.
		if tableAllowed(prefix, arg2) {
			return sqlite3.SQLITE_OK
		}
		return sqlite3.SQLITE_DENY
	case sqlite3.SQLITE_CREATE_INDEX, sqlite3.SQLITE_DROP_INDEX, sqlite3.SQLITE_REINDEX:
		if tableAllowed(prefix, arg2) {
			return sqlite3.SQLITE_OK
		}
		return sqlite3.SQLITE_DENY
	case sqlite3.SQLITE_CREATE_TRIGGER, sqlite3.SQLITE_DROP_TRIGGER,
		sqlite3.SQLITE_CREATE_VIEW, sqlite3.SQLITE_DROP_VIEW:
		// arg1 is the new object; arg2 is the table a trigger is on. Both must sit
		// inside the prefix so a trigger cannot fire on a core table.
		if tableAllowed(prefix, arg1) && (arg2 == "" || tableAllowed(prefix, arg2)) {
			return sqlite3.SQLITE_OK
		}
		return sqlite3.SQLITE_DENY
	default:
		return sqlite3.SQLITE_DENY
	}
}

func tableAllowed(prefix, name string) bool {
	if name == "" {
		return true
	}
	// SQLite may qualify as "main.table" or, while reparsing the schema, as
	// "temp.sqlite_temp_master".
	schema := "main"
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		schema = name[:i]
		name = name[i+1:]
		if schema == "" {
			schema = "main"
		}
		if !strings.EqualFold(schema, "main") && !strings.EqualFold(schema, "temp") {
			return false
		}
	}
	if strings.HasPrefix(strings.ToLower(name), "sqlite_") {
		lower := strings.ToLower(name)
		// SQLite implements DDL by writing sqlite_master / sqlite_schema. Denying
		// that would make CREATE TABLE impossible; other sqlite_* objects stay closed.
		// ALTER TABLE ... RENAME reparses the schema and reads the temp equivalents,
		// so those are readable too; temp objects themselves stay denied by action.
		switch lower {
		case "sqlite_master", "sqlite_schema":
			return true
		case "sqlite_temp_master", "sqlite_temp_schema":
			// The schema name arrives out of band (arg3), so accept either form.
			return true
		}
		return false
	}
	if !strings.EqualFold(schema, "main") {
		return false
	}
	// ALTER TABLE ADD COLUMN on STRICT tables runs PRAGMA quick_check, which reads
	// the eponymous virtual table.
	if strings.EqualFold(name, "pragma_quick_check") {
		return true
	}
	return strings.HasPrefix(name, prefix)
}

func wrapSQLDenied(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "not authorized") || strings.Contains(msg, "authorization denied") {
		return fmt.Errorf("%w: %v", ErrSQLDenied, err)
	}
	return err
}

// SetPluginAuthorizer installs (or clears, when prefix is empty) the prefix authorizer
// on the writer connection of the current transaction. pluginhost uses this so a
// CheckWorkTx read of core_plugin_state and plugin SQL can share one transaction.
func (s *Store) SetPluginAuthorizer(prefix string) error {
	if s.currentWriter == nil {
		return fmt.Errorf("storage: no writer connection in this transaction")
	}
	return setConnAuthorizer(s.currentWriter, prefix)
}
