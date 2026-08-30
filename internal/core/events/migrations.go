package events

import "github.com/hbaldwin98/control-center/internal/core/storage"

var migrations = []storage.Migration{
	{Version: 1, Name: "event_log", Up: `
CREATE TABLE core_events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    type       TEXT    NOT NULL,
    source     TEXT    NOT NULL,
    subject    TEXT    NOT NULL DEFAULT '',
    payload    TEXT    NOT NULL,
    created_at TEXT    NOT NULL
) STRICT;

CREATE INDEX core_events_type_id   ON core_events(type, id);
CREATE INDEX core_events_source_id ON core_events(source, id);

CREATE TABLE core_event_cursors (
    subscriber      TEXT    PRIMARY KEY,
    pattern         TEXT    NOT NULL,
    last_event_id   INTEGER NOT NULL DEFAULT 0,
    state           TEXT    NOT NULL DEFAULT 'active',
    failed_event_id INTEGER NOT NULL DEFAULT 0,
    attempts        INTEGER NOT NULL DEFAULT 0,
    retry_at        TEXT,
    last_error      TEXT    NOT NULL DEFAULT '',
    lease_owner     TEXT    NOT NULL DEFAULT '',
    lease_until     TEXT,
    updated_at      TEXT    NOT NULL
) STRICT;

-- The oldest event ID still retained. SSE compares a client's resume point against this
-- and emits a reset rather than a partial replay.
CREATE TABLE core_event_retention (
    id                 INTEGER PRIMARY KEY CHECK (id = 1),
    oldest_retained_id INTEGER NOT NULL,
    last_run_at        TEXT
) STRICT;

INSERT INTO core_event_retention(id, oldest_retained_id, last_run_at) VALUES (1, 1, NULL);
`},
}
