package pagewatch

import "github.com/hbaldwin98/control-center/host"

func (p *Plugin) Migrate(m host.Migrator) error {
	return m.Apply([]host.Migration{{
		Version: 1,
		Name:    "checks",
		Up: `
			CREATE TABLE pagewatch_state (
				id            INTEGER PRIMARY KEY CHECK (id = 1),
				url           TEXT    NOT NULL,
				content_hash  TEXT    NOT NULL,
				last_summary  TEXT    NOT NULL,
				last_ai_at    TEXT    NOT NULL,
				checked_at    TEXT    NOT NULL
			) STRICT;

			INSERT INTO pagewatch_state
				(id, url, content_hash, last_summary, last_ai_at, checked_at)
				VALUES (1, '', '', '', '', '');

			CREATE TABLE pagewatch_checks (
				id               INTEGER PRIMARY KEY,
				checked_at       TEXT    NOT NULL,
				url              TEXT    NOT NULL,
				status           TEXT    NOT NULL CHECK (status IN ('baseline', 'unchanged', 'changed', 'attention')),
				content_hash     TEXT    NOT NULL,
				expected_text    TEXT    NOT NULL,
				expected_found   INTEGER NOT NULL CHECK (expected_found IN (0, 1)),
				summary          TEXT    NOT NULL,
				ai_ran           INTEGER NOT NULL CHECK (ai_ran IN (0, 1)),
				input_tokens     INTEGER NOT NULL,
				output_tokens    INTEGER NOT NULL,
				cost_micro_usd   INTEGER NOT NULL,
				browser_ms       INTEGER NOT NULL,
				ai_ms            INTEGER NOT NULL,
				job_id           INTEGER NOT NULL,
				event_id         INTEGER NOT NULL UNIQUE
			) STRICT;
		`,
	}})
}
