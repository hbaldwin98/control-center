package policy

import "github.com/hbaldwin98/control-center/internal/core/storage"

// Spend rollups and reservations are authoritative policy state; core_ai_calls and
// core_ai_attempts remain the call-level audit record.
var migrations = []storage.Migration{
	{Version: 1, Name: "plugin_state_and_budgets", Up: `
CREATE TABLE core_plugin_state (
    plugin_id            TEXT    PRIMARY KEY,
    enabled              INTEGER NOT NULL DEFAULT 0,
    automated            INTEGER NOT NULL DEFAULT 0,
    disabled_at          TEXT,
    disabled_by          TEXT    NOT NULL DEFAULT '',
    disabled_reason      TEXT    NOT NULL DEFAULT '',
    accounting_failed_at TEXT
) STRICT;

CREATE TABLE core_plugin_budget (
    plugin_id        TEXT    PRIMARY KEY REFERENCES core_plugin_state(plugin_id) ON DELETE CASCADE,
    hourly_microusd  INTEGER NOT NULL DEFAULT 0,
    daily_microusd   INTEGER NOT NULL DEFAULT 0,
    monthly_microusd INTEGER NOT NULL DEFAULT 0,
    on_exceed        TEXT    NOT NULL DEFAULT 'reject'
) STRICT;

-- Committed spend, rolled up per plugin, window, and UTC period.
CREATE TABLE core_plugin_spend (
    plugin_id          TEXT    NOT NULL,
    window             TEXT    NOT NULL,
    period_start       TEXT    NOT NULL,
    committed_microusd INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (plugin_id, window, period_start)
) STRICT;

-- One row per admitted paid call. Persisted, so concurrent calls cannot consume the same
-- capacity and a crash cannot silently undercount.
CREATE TABLE core_plugin_spend_reservations (
    id              TEXT    PRIMARY KEY,
    plugin_id       TEXT    NOT NULL,
    maximum_microusd INTEGER NOT NULL,
    admitted_at     TEXT    NOT NULL,
    hour_start      TEXT    NOT NULL,
    day_start       TEXT    NOT NULL,
    month_start     TEXT    NOT NULL
) STRICT;

CREATE INDEX core_plugin_spend_reservations_plugin
    ON core_plugin_spend_reservations(plugin_id);

-- Unique per plugin, window, and period, so repeated rejected calls cannot flood alerts.
CREATE TABLE core_plugin_budget_events (
    plugin_id    TEXT    NOT NULL,
    window       TEXT    NOT NULL,
    period_start TEXT    NOT NULL,
    event_id     INTEGER NOT NULL,
    PRIMARY KEY (plugin_id, window, period_start)
) STRICT;
`},
}
