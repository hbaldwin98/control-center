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
	}, {
		Version: 2,
		Name:    "billing_history_and_peak_usage",
		Up: `
			ALTER TABLE tid_readings ADD COLUMN on_peak_kwh REAL;
			ALTER TABLE tid_readings ADD COLUMN off_peak_kwh REAL;
			CREATE TABLE tid_billing_periods (
				period_start TEXT NOT NULL,
				period_end TEXT NOT NULL,
				peak_demand_date TEXT,
				peak_demand_kw REAL,
				collected_at TEXT NOT NULL,
				PRIMARY KEY(period_start, period_end)
			) STRICT;
			CREATE TABLE tid_period_readings (
				period_start TEXT NOT NULL,
				period_end TEXT NOT NULL,
				day TEXT NOT NULL,
				kwh REAL NOT NULL,
				cost_cents INTEGER,
				on_peak_kwh REAL,
				off_peak_kwh REAL,
				PRIMARY KEY(period_start, period_end, day)
			) STRICT;
			CREATE INDEX tid_period_readings_day ON tid_period_readings(day);
		`,
	}, {
		Version: 3,
		Name:    "normalize_billing_period_dates",
		Up: `
			CREATE TABLE tid_billing_periods_normalized (
				period_start TEXT NOT NULL,
				period_end TEXT NOT NULL,
				peak_demand_date TEXT,
				peak_demand_kw REAL,
				collected_at TEXT NOT NULL,
				PRIMARY KEY(period_start, period_end)
			) STRICT;
			INSERT INTO tid_billing_periods_normalized
			SELECT substr(period_start, 1, 10), substr(period_end, 1, 10),
			       MAX(peak_demand_date), MAX(peak_demand_kw), MAX(collected_at)
			  FROM tid_billing_periods
			 GROUP BY substr(period_start, 1, 10), substr(period_end, 1, 10);

			CREATE TABLE tid_period_readings_normalized (
				period_start TEXT NOT NULL,
				period_end TEXT NOT NULL,
				day TEXT NOT NULL,
				kwh REAL NOT NULL,
				cost_cents INTEGER,
				on_peak_kwh REAL,
				off_peak_kwh REAL,
				PRIMARY KEY(period_start, period_end, day)
			) STRICT;
			INSERT INTO tid_period_readings_normalized
			SELECT substr(period_start, 1, 10), substr(period_end, 1, 10), day,
			       MAX(kwh), MAX(cost_cents), MAX(on_peak_kwh), MAX(off_peak_kwh)
			  FROM tid_period_readings
			 GROUP BY substr(period_start, 1, 10), substr(period_end, 1, 10), day;

			DROP TABLE tid_period_readings;
			DROP TABLE tid_billing_periods;
			ALTER TABLE tid_billing_periods_normalized RENAME TO tid_billing_periods;
			ALTER TABLE tid_period_readings_normalized RENAME TO tid_period_readings;
			CREATE INDEX tid_period_readings_day ON tid_period_readings(day);
		`,
	}})
}
