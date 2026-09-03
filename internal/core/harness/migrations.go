package harness

import "github.com/hbaldwin98/control-center/internal/core/storage"

var migrations = []storage.Migration{
	{Version: 1, Name: "harness_sessions", Up: `
CREATE TABLE core_harness_sessions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id   TEXT    NOT NULL,
    profile_name TEXT    NOT NULL,
    title        TEXT    NOT NULL,
    workspace    TEXT    NOT NULL,
    state        TEXT    NOT NULL,
    exit_code    INTEGER,
    error        TEXT    NOT NULL DEFAULT '',
    stop_reason  TEXT    NOT NULL DEFAULT '',
    created_at   TEXT    NOT NULL,
    started_at   TEXT,
    finished_at  TEXT
) STRICT;

CREATE INDEX core_harness_sessions_created
    ON core_harness_sessions(created_at DESC, id DESC);

CREATE TABLE core_harness_output (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id INTEGER NOT NULL,
    stream     TEXT    NOT NULL,
    text       TEXT    NOT NULL,
    bytes      INTEGER NOT NULL,
    created_at TEXT    NOT NULL,
    FOREIGN KEY (session_id) REFERENCES core_harness_sessions(id) ON DELETE CASCADE
) STRICT;

CREATE INDEX core_harness_output_session
    ON core_harness_output(session_id, id);
`},
}
