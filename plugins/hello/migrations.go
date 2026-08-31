package hello

import "github.com/hbaldwin98/control-center/host"

func (p *Plugin) Migrate(m host.Migrator) error {
	return m.Apply([]host.Migration{{
		Version: 1,
		Name:    "ticks",
		Up: `
			CREATE TABLE hello_ticks (
				id INTEGER PRIMARY KEY,
				at TEXT NOT NULL,
				note TEXT NOT NULL,
				blob_key TEXT NOT NULL DEFAULT '',
				ai_text TEXT NOT NULL DEFAULT '',
				event_id INTEGER NOT NULL DEFAULT 0
			) STRICT;
		`,
	}})
}
