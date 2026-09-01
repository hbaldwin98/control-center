package bidrl

import "github.com/hbaldwin98/control-center/host"

func (p *Plugin) Migrate(m host.Migrator) error {
	return m.Apply([]host.Migration{{
		Version: 1,
		Name:    "lots",
		Up: `
			CREATE TABLE bidrl_auctions (
				id            TEXT PRIMARY KEY,
				url           TEXT NOT NULL UNIQUE,
				title         TEXT NOT NULL,
				host          TEXT NOT NULL,
				lot_count     INTEGER NOT NULL DEFAULT 0,
				status        TEXT NOT NULL CHECK (status IN ('pending','collecting','ready','failed')),
				last_error    TEXT NOT NULL DEFAULT '',
				collected_at  TEXT NOT NULL DEFAULT '',
				created_at    TEXT NOT NULL
			) STRICT;

			CREATE TABLE bidrl_lots (
				id                 TEXT PRIMARY KEY,
				auction_id         TEXT NOT NULL,
				url                TEXT NOT NULL,
				lot_code           TEXT NOT NULL DEFAULT '',
				title              TEXT NOT NULL,
				current_bid_cents  INTEGER,
				currency           TEXT NOT NULL DEFAULT 'USD',
				bucket             TEXT NOT NULL DEFAULT 'pending'
					CHECK (bucket IN ('pending','skipped','research','priced','worth_opening','discarded','rejected')),
				reject_reason      TEXT NOT NULL DEFAULT '',
				created_at        TEXT NOT NULL
			) STRICT;
			CREATE INDEX bidrl_lots_auction ON bidrl_lots(auction_id);
			CREATE INDEX bidrl_lots_bucket ON bidrl_lots(bucket);

			CREATE TABLE bidrl_images (
				lot_id      TEXT NOT NULL,
				ordinal     INTEGER NOT NULL,
				blob_key    TEXT NOT NULL,
				source_url  TEXT NOT NULL,
				mime        TEXT NOT NULL,
				size_bytes  INTEGER NOT NULL,
				PRIMARY KEY (lot_id, ordinal)
			) STRICT;

			CREATE TABLE bidrl_analyses (
				id               INTEGER PRIMARY KEY,
				lot_id           TEXT NOT NULL,
				identification   TEXT NOT NULL,
				basis            TEXT NOT NULL,
				model_or_sku     TEXT NOT NULL DEFAULT '',
				title_agreement  REAL NOT NULL,
				notes            TEXT NOT NULL DEFAULT '',
				input_tokens     INTEGER NOT NULL,
				output_tokens    INTEGER NOT NULL,
				cost_micro_usd   INTEGER NOT NULL,
				created_at       TEXT NOT NULL,
				event_id         INTEGER NOT NULL
			) STRICT;
			CREATE INDEX bidrl_analyses_lot ON bidrl_analyses(lot_id);

			CREATE TABLE bidrl_valuations (
				id                   INTEGER PRIMARY KEY,
				lot_id               TEXT NOT NULL,
				price_cents          INTEGER NOT NULL,
				currency             TEXT NOT NULL,
				condition            TEXT NOT NULL,
				kind                 TEXT NOT NULL CHECK (kind IN ('asking','sold')),
				model_or_code        TEXT NOT NULL,
				cited_text           TEXT NOT NULL,
				source_url           TEXT NOT NULL,
				source_title         TEXT NOT NULL,
				source_published_at  TEXT NOT NULL DEFAULT '',
				retrieved_at        TEXT NOT NULL,
				created_at           TEXT NOT NULL,
				event_id             INTEGER NOT NULL
			) STRICT;
			CREATE INDEX bidrl_valuations_lot ON bidrl_valuations(lot_id);

			CREATE TABLE bidrl_rejections (
				id          INTEGER PRIMARY KEY,
				auction_id  TEXT NOT NULL,
				lot_id      TEXT NOT NULL DEFAULT '',
				reason      TEXT NOT NULL,
				detail      TEXT NOT NULL,
				created_at  TEXT NOT NULL
			) STRICT;
			CREATE TABLE bidrl_meta (
				id             INTEGER PRIMARY KEY CHECK (id = 1),
				last_event_id  INTEGER NOT NULL DEFAULT 0
			) STRICT;
			INSERT INTO bidrl_meta(id, last_event_id) VALUES (1, 0);
		`,
	}})
}
