package ai

import "github.com/hbaldwin98/control-center/internal/core/storage"

var migrations = []storage.Migration{
	{Version: 1, Name: "calls_and_attempts", Up: `
CREATE TABLE core_ai_calls (
    id                   TEXT PRIMARY KEY,
    reservation_id       TEXT NOT NULL,
    plugin_id            TEXT NOT NULL,
    job_id               TEXT NOT NULL DEFAULT '',
    source_event_id      TEXT NOT NULL DEFAULT '',
    usage_event_id       INTEGER,
    operation            TEXT NOT NULL,
    logical_model        TEXT NOT NULL,
    status               TEXT NOT NULL,
    error_class          TEXT NOT NULL DEFAULT '',
    reserved_micro_usd   INTEGER NOT NULL,
    settled_micro_usd    INTEGER NOT NULL DEFAULT 0,
    started_at           TEXT NOT NULL,
    finalized_at         TEXT
) STRICT;

CREATE INDEX core_ai_calls_plugin ON core_ai_calls(plugin_id, started_at);
CREATE INDEX core_ai_calls_status ON core_ai_calls(status, started_at);

CREATE TABLE core_ai_attempts (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    call_id          TEXT    NOT NULL,
    ordinal          INTEGER NOT NULL,
    provider         TEXT    NOT NULL,
    provider_model   TEXT    NOT NULL,
    status           TEXT    NOT NULL,
    provider_status  TEXT    NOT NULL DEFAULT '',
    error_class      TEXT    NOT NULL DEFAULT '',
    billing_state    TEXT    NOT NULL DEFAULT '',
    input_tokens     INTEGER NOT NULL DEFAULT 0,
    output_tokens    INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens INTEGER NOT NULL DEFAULT 0,
    cached_tokens    INTEGER NOT NULL DEFAULT 0,
    image_count      INTEGER NOT NULL DEFAULT 0,
    search_queries   INTEGER NOT NULL DEFAULT 0,
    cost_micro_usd   INTEGER NOT NULL DEFAULT 0,
    latency_ms       INTEGER NOT NULL DEFAULT 0,
    UNIQUE (call_id, ordinal)
) STRICT;
`},
}
