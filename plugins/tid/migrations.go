package tid

import "github.com/hbaldwin98/control-center/host"

func (p *Plugin) Migrate(m host.Migrator) error {
	return m.Apply([]host.Migration{{
		Version: 1,
		Name:    "readings",
		Up: `
			CREATE TABLE tid_readings (
				day TEXT PRIMARY KEY,
				kwh REAL NOT NULL,
				cost_cents INTEGER,
				source TEXT NOT NULL,
				collected_at TEXT NOT NULL
			) STRICT;
			CREATE TABLE tid_insights (
				id INTEGER PRIMARY KEY,
				at TEXT NOT NULL,
				summary TEXT NOT NULL,
				recommendation TEXT NOT NULL DEFAULT '',
				anomalies TEXT NOT NULL DEFAULT '[]',
				event_id INTEGER NOT NULL DEFAULT 0
			) STRICT;
			CREATE TABLE tid_syncs (
				id INTEGER PRIMARY KEY,
				at TEXT NOT NULL,
				status TEXT NOT NULL,
				rows INTEGER NOT NULL DEFAULT 0,
				source TEXT NOT NULL DEFAULT '',
				error TEXT NOT NULL DEFAULT '',
				event_id INTEGER NOT NULL DEFAULT 0
			) STRICT;
		`,
	}})
}
