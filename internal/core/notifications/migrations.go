package notifications

import "github.com/hbaldwin98/control-center/internal/core/storage"

var migrations = []storage.Migration{
	{Version: 1, Name: "notifications_core", Up: `
CREATE TABLE core_notification_rules (
    id          TEXT    PRIMARY KEY,
    enabled     INTEGER NOT NULL DEFAULT 1,
    match       TEXT    NOT NULL,
    where_expr  TEXT    NOT NULL DEFAULT '',
    channels    TEXT    NOT NULL,
    title       TEXT    NOT NULL,
    body        TEXT    NOT NULL DEFAULT '',
    url         TEXT    NOT NULL DEFAULT '',
    throttle_s  INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE TABLE core_notification_channels (
    id            TEXT    PRIMARY KEY,
    kind          TEXT    NOT NULL,
    config        TEXT    NOT NULL DEFAULT '{}',
    credential_id TEXT    NOT NULL DEFAULT '',
    enabled       INTEGER NOT NULL DEFAULT 1
) STRICT;

CREATE TABLE core_notification_throttles (
    rule_id           TEXT    NOT NULL,
    subject_source    TEXT    NOT NULL,
    subject_value     TEXT    NOT NULL,
    window_started_at TEXT    NOT NULL,
    window_ends_at    TEXT    NOT NULL,
    notification_id   TEXT    NOT NULL,
    PRIMARY KEY (rule_id, subject_source, subject_value, window_started_at)
) STRICT;

CREATE INDEX core_notification_throttles_open
    ON core_notification_throttles(rule_id, subject_source, subject_value, window_ends_at);

CREATE TABLE core_notifications (
    id              TEXT    PRIMARY KEY,
    source_event_id INTEGER NOT NULL,
    uniqueness_key  TEXT    NOT NULL UNIQUE,
    rule_id         TEXT    NOT NULL,
    subject_source  TEXT    NOT NULL,
    subject_value   TEXT    NOT NULL,
    title           TEXT    NOT NULL,
    body            TEXT    NOT NULL,
    url             TEXT    NOT NULL,
    collapsed_count INTEGER NOT NULL DEFAULT 0,
    available_at    TEXT    NOT NULL,
    created_at      TEXT    NOT NULL,
    read_at         TEXT,
    in_inbox        INTEGER NOT NULL DEFAULT 1,
    ready_published INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX core_notifications_inbox
    ON core_notifications(in_inbox, available_at, created_at, id);
CREATE INDEX core_notifications_ready
    ON core_notifications(ready_published, available_at);

CREATE TABLE core_notification_sources (
    notification_id TEXT    NOT NULL,
    event_id        INTEGER NOT NULL,
    PRIMARY KEY (notification_id, event_id)
) STRICT;

CREATE TABLE core_notification_sends (
    id              TEXT    PRIMARY KEY,
    notification_id TEXT    NOT NULL,
    channel_id      TEXT    NOT NULL,
    idempotency_key TEXT    NOT NULL UNIQUE,
    state           TEXT    NOT NULL,
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT    NOT NULL,
    lease_until     TEXT,
    last_error      TEXT    NOT NULL DEFAULT '',
    sent_at         TEXT,
    last_attempt_at TEXT
) STRICT;

CREATE INDEX core_notification_sends_claim
    ON core_notification_sends(state, next_attempt_at);

CREATE TABLE core_notification_audit (
    id          TEXT    PRIMARY KEY,
    actor       TEXT    NOT NULL,
    object_kind TEXT    NOT NULL,
    object_id   TEXT    NOT NULL,
    action      TEXT    NOT NULL,
    created_at  TEXT    NOT NULL
) STRICT;
`},
	{Version: 2, Name: "plugin_alert_copy", Up: `
UPDATE core_notification_rules
   SET title = '{event.subject}', body = '{event.payload.body}'
 WHERE id = 'plugin-alert'
   AND title = '{event.type}'
   AND body = '{event.subject}';
`},
}
