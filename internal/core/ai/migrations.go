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
	{Version: 2, Name: "providers_catalog_and_routes", Up: `
CREATE TABLE core_ai_providers (
    id            TEXT PRIMARY KEY,
    kind          TEXT NOT NULL,
    base_url      TEXT NOT NULL DEFAULT '',
    credential_id TEXT NOT NULL,
    billing       TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
) STRICT;

-- The discovered model catalog. Rows are a cache of what a provider says it offers;
-- nothing dispatches from here. A route attempt names its model explicitly, so a
-- provider that stops listing a model breaks discovery, not an existing route.
CREATE TABLE core_ai_catalog (
    provider_id                  TEXT    NOT NULL,
    model                        TEXT    NOT NULL,
    display_name                 TEXT    NOT NULL DEFAULT '',
    context_window               INTEGER NOT NULL DEFAULT 0,
    max_output_tokens            INTEGER NOT NULL DEFAULT 0,
    input_micro_usd_per_million  INTEGER NOT NULL DEFAULT 0,
    output_micro_usd_per_million INTEGER NOT NULL DEFAULT 0,
    priced                       INTEGER NOT NULL DEFAULT 0,
    fetched_at                   TEXT    NOT NULL,
    PRIMARY KEY (provider_id, model)
) STRICT;

CREATE TABLE core_ai_routes (
    name              TEXT    PRIMARY KEY,
    capabilities      TEXT    NOT NULL,
    max_input_tokens  INTEGER NOT NULL,
    max_output_tokens INTEGER NOT NULL,
    updated_at        TEXT    NOT NULL
) STRICT;

-- Pricing is copied onto the attempt rather than read from the catalog at dispatch.
-- A reservation must mean the same thing tomorrow as it did when it was written, and a
-- catalog refresh must never silently reprice an admitted call.
CREATE TABLE core_ai_route_attempts (
    route_name                   TEXT    NOT NULL,
    ordinal                      INTEGER NOT NULL,
    provider_id                  TEXT    NOT NULL,
    model                        TEXT    NOT NULL,
    input_micro_usd_per_million  INTEGER NOT NULL DEFAULT 0,
    output_micro_usd_per_million INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (route_name, ordinal)
) STRICT;
`},
	// The provider's own words about a failure. error_class says which bucket a failure
	// fell into; it cannot say that a model rejected a parameter or that an id was
	// unknown. Without this the only record of a 400 is the word "provider".
	{Version: 3, Name: "attempt_provider_error", Up: `
ALTER TABLE core_ai_attempts ADD COLUMN provider_error TEXT NOT NULL DEFAULT '';
`},
}
