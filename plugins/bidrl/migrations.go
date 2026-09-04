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
	}, {
		Version: 2,
		Name:    "search",
		Up: `
			CREATE TABLE bidrl_affiliate_auctions (
				id              TEXT PRIMARY KEY,
				url             TEXT NOT NULL,
				title           TEXT NOT NULL,
				affiliate_id    TEXT NOT NULL,
				affiliate_name  TEXT NOT NULL,
				city            TEXT NOT NULL DEFAULT '',
				item_count      INTEGER NOT NULL DEFAULT 0,
				ends_at         TEXT NOT NULL DEFAULT '',
				seen_at         TEXT NOT NULL
			) STRICT;
			CREATE INDEX bidrl_affiliate_auctions_aff ON bidrl_affiliate_auctions(affiliate_id);

			CREATE TABLE bidrl_searches (
				id          TEXT PRIMARY KEY,
				query       TEXT NOT NULL,
				scope       TEXT NOT NULL,
				status      TEXT NOT NULL,
				hit_count   INTEGER NOT NULL DEFAULT 0,
				last_error  TEXT NOT NULL DEFAULT '',
				created_at  TEXT NOT NULL
			) STRICT;

			CREATE TABLE bidrl_search_hits (
				search_id       TEXT NOT NULL,
				ordinal         INTEGER NOT NULL,
				lot_id          TEXT NOT NULL,
				auction_id      TEXT NOT NULL,
				url             TEXT NOT NULL,
				title           TEXT NOT NULL,
				auction_title   TEXT NOT NULL DEFAULT '',
				lot_code        TEXT NOT NULL DEFAULT '',
				affiliate_id    TEXT NOT NULL DEFAULT '',
				affiliate_name  TEXT NOT NULL DEFAULT '',
				preferred       INTEGER NOT NULL DEFAULT 0,
				bid_cents       INTEGER,
				match_score     REAL NOT NULL,
				match_reason    TEXT NOT NULL DEFAULT '',
				source          TEXT NOT NULL,
				PRIMARY KEY (search_id, ordinal)
			) STRICT;
			CREATE INDEX bidrl_search_hits_search ON bidrl_search_hits(search_id);
		`,
	}, {
		Version: 3,
		Name:    "itemdata",
		Up: `
			ALTER TABLE bidrl_auctions ADD COLUMN ends_at TEXT NOT NULL DEFAULT '';
			ALTER TABLE bidrl_lots ADD COLUMN ends_at TEXT NOT NULL DEFAULT '';
			ALTER TABLE bidrl_lots ADD COLUMN bid_count INTEGER NOT NULL DEFAULT 0;
			ALTER TABLE bidrl_lots ADD COLUMN high_bidder TEXT NOT NULL DEFAULT '';
			ALTER TABLE bidrl_lots ADD COLUMN min_bid_cents INTEGER;
			ALTER TABLE bidrl_lots ADD COLUMN bid_increment_cents INTEGER;
			ALTER TABLE bidrl_lots ADD COLUMN bidding_extended INTEGER NOT NULL DEFAULT 0;
			ALTER TABLE bidrl_lots ADD COLUMN reserve_met INTEGER NOT NULL DEFAULT 0;
			ALTER TABLE bidrl_lots ADD COLUMN category TEXT NOT NULL DEFAULT '';
			ALTER TABLE bidrl_lots ADD COLUMN description TEXT NOT NULL DEFAULT '';
			ALTER TABLE bidrl_lots ADD COLUMN itemdata_at TEXT NOT NULL DEFAULT '';
			ALTER TABLE bidrl_lots ADD COLUMN bids_refreshed_at TEXT NOT NULL DEFAULT '';
			ALTER TABLE bidrl_analyses ADD COLUMN category TEXT NOT NULL DEFAULT '';
			ALTER TABLE bidrl_analyses ADD COLUMN search_terms TEXT NOT NULL DEFAULT '[]';
		`,
	}, {
		Version: 4,
		Name:    "reuse_comps",
		Up: `
			ALTER TABLE bidrl_valuations ADD COLUMN reused_from_lot_id TEXT NOT NULL DEFAULT '';
			CREATE INDEX bidrl_valuations_model ON bidrl_valuations(model_or_code);
		`,
	}, {
		Version: 5,
		Name:    "intent",
		Up: `
			CREATE TABLE bidrl_intent_searches (
				id          TEXT PRIMARY KEY,
				query       TEXT NOT NULL,
				status      TEXT NOT NULL,
				scanned     INTEGER NOT NULL DEFAULT 0,
				skipped     INTEGER NOT NULL DEFAULT 0,
				hit_count   INTEGER NOT NULL DEFAULT 0,
				last_error  TEXT NOT NULL DEFAULT '',
				created_at  TEXT NOT NULL
			) STRICT;

			CREATE TABLE bidrl_intent_hits (
				search_id  TEXT NOT NULL,
				ordinal    INTEGER NOT NULL,
				lot_id     TEXT NOT NULL,
				score      REAL NOT NULL,
				reason     TEXT NOT NULL DEFAULT '',
				PRIMARY KEY (search_id, ordinal)
			) STRICT;
			CREATE INDEX bidrl_intent_hits_search ON bidrl_intent_hits(search_id);
			CREATE INDEX bidrl_intent_hits_lot ON bidrl_intent_hits(lot_id);
		`,
	}, {
		Version: 6,
		Name:    "lot_embeddings",
		Up: `
			CREATE TABLE bidrl_lot_embeddings (
				lot_id      TEXT PRIMARY KEY,
				text_hash   TEXT NOT NULL,
				dims        INTEGER NOT NULL,
				vector      BLOB NOT NULL,
				updated_at  TEXT NOT NULL
			) STRICT;
		`,
	}, {
		Version: 7,
		Name:    "auction_location",
		Up: `
			ALTER TABLE bidrl_auctions ADD COLUMN affiliate_id TEXT NOT NULL DEFAULT '';
			ALTER TABLE bidrl_auctions ADD COLUMN affiliate_name TEXT NOT NULL DEFAULT '';
			ALTER TABLE bidrl_auctions ADD COLUMN city TEXT NOT NULL DEFAULT '';
			CREATE INDEX bidrl_auctions_affiliate ON bidrl_auctions(affiliate_id);

			-- Location used to be a read-time join against the SITES discovery cache,
			-- which discover wipes and rebuilds on every run, so a collected auction lost
			-- its location the moment it closed. Carry across whatever that cache still
			-- holds; anything already gone fills in on the next discover.
			UPDATE bidrl_auctions SET
				affiliate_id   = IFNULL((SELECT s.affiliate_id   FROM bidrl_affiliate_auctions s WHERE s.id = bidrl_auctions.id), ''),
				affiliate_name = IFNULL((SELECT s.affiliate_name FROM bidrl_affiliate_auctions s WHERE s.id = bidrl_auctions.id), ''),
				city           = IFNULL((SELECT s.city           FROM bidrl_affiliate_auctions s WHERE s.id = bidrl_auctions.id), '');
		`,
	}, {
		Version: 8,
		Name:    "favorites",
		Up: `
			CREATE TABLE bidrl_favorites (
				lot_id      TEXT PRIMARY KEY,
				note        TEXT NOT NULL DEFAULT '',
				created_at  TEXT NOT NULL
			) STRICT;
		`,
	}, {
		Version: 9,
		Name:    "watchlists",
		Up: `
			CREATE TABLE bidrl_watchlists (
				id             TEXT PRIMARY KEY,
				name           TEXT NOT NULL,
				query          TEXT NOT NULL,
				enabled        INTEGER NOT NULL DEFAULT 1,
				affiliate_ids  TEXT NOT NULL DEFAULT '[]',
				categories     TEXT NOT NULL DEFAULT '[]',
				max_bid_cents  INTEGER,
				min_score      REAL NOT NULL DEFAULT 0.55,
				expansion      TEXT NOT NULL DEFAULT '[]',
				expanded_at    TEXT NOT NULL DEFAULT '',
				status         TEXT NOT NULL DEFAULT 'idle',
				last_error     TEXT NOT NULL DEFAULT '',
				last_run_at    TEXT NOT NULL DEFAULT '',
				created_at     TEXT NOT NULL
			) STRICT;

			CREATE TABLE bidrl_findings (
				id            TEXT PRIMARY KEY,
				watchlist_id  TEXT NOT NULL,
				lot_id        TEXT NOT NULL,
				score         REAL NOT NULL,
				reason        TEXT NOT NULL DEFAULT '',
				state         TEXT NOT NULL DEFAULT 'new'
					CHECK (state IN ('new','accepted','rejected')),
				decided_at    TEXT NOT NULL DEFAULT '',
				created_at    TEXT NOT NULL,
				UNIQUE (watchlist_id, lot_id)
			) STRICT;
			CREATE INDEX bidrl_findings_state ON bidrl_findings(state, created_at);
			CREATE INDEX bidrl_findings_lot ON bidrl_findings(lot_id);
		`,
	}, {
		Version: 10,
		Name:    "correct_ends_at",
		Up:      rewriteStoredEndsAtSQL,
	}, {
		Version: 11,
		Name:    "automation",
		Up: `
			CREATE TABLE bidrl_automation (
				id               INTEGER PRIMARY KEY CHECK (id = 1),
				last_sweep_at    TEXT NOT NULL DEFAULT '',
				last_sweep_note  TEXT NOT NULL DEFAULT '',
				last_match_at    TEXT NOT NULL DEFAULT '',
				last_match_note  TEXT NOT NULL DEFAULT '',
				throttled_until  TEXT NOT NULL DEFAULT ''
			) STRICT;
			INSERT INTO bidrl_automation(id) VALUES (1);
		`,
	}, {
		Version: 12,
		Name:    "high_bidder_id",
		Up: `
			ALTER TABLE bidrl_lots ADD COLUMN high_bidder_id TEXT NOT NULL DEFAULT '';
		`,
	}, {
		Version: 13,
		Name:    "ending_soon_alert",
		Up: `
			ALTER TABLE bidrl_lots ADD COLUMN ending_soon_alerted_at TEXT NOT NULL DEFAULT '';
		`,
	}, {
		Version: 14,
		Name:    "auction_hidden",
		Up: `
			ALTER TABLE bidrl_auctions ADD COLUMN hidden INTEGER NOT NULL DEFAULT 0;
		`,
	}})
}

// rewriteStoredEndsAtSQL is the one-time backfill for close times written by the
// old parser. Collected lots stored BidRL unix as UTC (two hours early).
// SITES listings stored a UTC wall clock labeled +0300. New writes already go
// through parseEndTime; this SQL must not run against those (it is versioned).
const rewriteStoredEndsAtSQL = `
			UPDATE bidrl_lots
			SET ends_at = strftime('%Y-%m-%dT%H:%M:%SZ', ends_at, '+2 hours')
			WHERE ends_at LIKE '%Z'
			  AND strftime('%Y-%m-%dT%H:%M:%SZ', ends_at, '+2 hours') IS NOT NULL;

			UPDATE bidrl_auctions
			SET ends_at = strftime('%Y-%m-%dT%H:%M:%SZ', ends_at, '+2 hours')
			WHERE ends_at LIKE '%Z'
			  AND strftime('%Y-%m-%dT%H:%M:%SZ', ends_at, '+2 hours') IS NOT NULL;

			UPDATE bidrl_lots
			SET ends_at = substr(ends_at, 1, 19) || 'Z'
			WHERE substr(ends_at, 20) GLOB '[+-][0-9][0-9][0-9][0-9]';

			UPDATE bidrl_auctions
			SET ends_at = substr(ends_at, 1, 19) || 'Z'
			WHERE substr(ends_at, 20) GLOB '[+-][0-9][0-9][0-9][0-9]';

			UPDATE bidrl_affiliate_auctions
			SET ends_at = substr(ends_at, 1, 19) || 'Z'
			WHERE substr(ends_at, 20) GLOB '[+-][0-9][0-9][0-9][0-9]';
		`
