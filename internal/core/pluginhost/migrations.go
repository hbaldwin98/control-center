package pluginhost

import "github.com/hbaldwin98/control-center/internal/core/storage"

var migrations = []storage.Migration{
	{Version: 1, Name: "config", Up: `
CREATE TABLE core_plugin_config (
    plugin_id  TEXT    NOT NULL PRIMARY KEY,
    value_json TEXT    NOT NULL,
    version    INTEGER NOT NULL,
    updated_at TEXT    NOT NULL,
    updated_by TEXT    NOT NULL
) STRICT;
`},
}
