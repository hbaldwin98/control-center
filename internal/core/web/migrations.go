package web

import "github.com/hbaldwin98/control-center/internal/core/storage"

// Migrations owned by the web module: the single administrator principal, the one-time
// bootstrap token, and sessions.
var migrations = []storage.Migration{
	{Version: 1, Name: "admin_and_sessions", Up: `
CREATE TABLE core_admin (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    password_hash TEXT    NOT NULL,
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL
) STRICT;

-- A single-use bootstrap token, offered only on loopback before an admin exists.
CREATE TABLE core_bootstrap_token (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    token_hash BLOB    NOT NULL,
    created_at TEXT    NOT NULL,
    used_at    TEXT
) STRICT;

CREATE TABLE core_sessions (
    id           TEXT    PRIMARY KEY,
    token_hash   BLOB    NOT NULL,
    csrf_hash    BLOB    NOT NULL,
    created_at   TEXT    NOT NULL,
    last_seen_at TEXT    NOT NULL,
    expires_at   TEXT    NOT NULL,
    reauth_at    TEXT
) STRICT;

CREATE INDEX core_sessions_expiry ON core_sessions(expires_at);
`},
}
