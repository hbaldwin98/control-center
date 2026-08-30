package jobs

import "github.com/hbaldwin98/control-center/internal/core/storage"

var migrations = []storage.Migration{
	{Version: 1, Name: "jobs_queue", Up: `
CREATE TABLE core_jobs (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    plugin_id        TEXT    NOT NULL,
    name             TEXT    NOT NULL,
    args             TEXT    NOT NULL,
    state            TEXT    NOT NULL,
    attempt          INTEGER NOT NULL DEFAULT 0,
    max_attempts     INTEGER NOT NULL,
    concurrency      INTEGER NOT NULL DEFAULT 1,
    priority         INTEGER NOT NULL DEFAULT 0,
    idempotency_key  TEXT,
    run_at           TEXT    NOT NULL,
    next_retry_at    TEXT,
    lease_owner      TEXT    NOT NULL DEFAULT '',
    lease_expires_at TEXT,
    fence_generation INTEGER NOT NULL DEFAULT 0,
    cancel_reason    TEXT    NOT NULL DEFAULT '',
    started_at       TEXT,
    finished_at      TEXT,
    progress         REAL    NOT NULL DEFAULT 0,
    progress_message TEXT    NOT NULL DEFAULT '',
    last_error       TEXT    NOT NULL DEFAULT '',
    last_error_class TEXT    NOT NULL DEFAULT '',
    created_at       TEXT    NOT NULL
) STRICT;

CREATE INDEX core_jobs_claim
    ON core_jobs(state, priority, run_at, id);

CREATE UNIQUE INDEX core_jobs_idempotency
    ON core_jobs(plugin_id, name, idempotency_key)
    WHERE idempotency_key IS NOT NULL
      AND state IN ('pending', 'running', 'retry_wait', 'cancel_requested');

CREATE TABLE core_job_logs (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id           INTEGER NOT NULL,
    attempt          INTEGER NOT NULL,
    fence_generation INTEGER NOT NULL,
    at               TEXT    NOT NULL,
    line             TEXT    NOT NULL
) STRICT;

CREATE INDEX core_job_logs_job ON core_job_logs(job_id, id);

CREATE TABLE core_job_schedules (
    plugin_id  TEXT NOT NULL,
    job_name   TEXT NOT NULL,
    timezone   TEXT NOT NULL,
    last_slot  TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL,
    PRIMARY KEY (plugin_id, job_name)
) STRICT;
`},
}
