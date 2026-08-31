package credentials

import "github.com/hbaldwin98/control-center/internal/core/storage"

var migrations = []storage.Migration{
	{Version: 1, Name: "credentials", Up: `
CREATE TABLE core_credentials (
    id              TEXT    PRIMARY KEY,
    kind            TEXT    NOT NULL,
    provider        TEXT    NOT NULL,
    status          TEXT    NOT NULL,
    version         INTEGER NOT NULL,
    expires_at      TEXT,
    scopes          TEXT    NOT NULL DEFAULT '[]',
    secret_envelope TEXT    NOT NULL,
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL
) STRICT;

CREATE TABLE core_credential_oauth_states (
    state_hash        TEXT PRIMARY KEY,
    session_hash      TEXT NOT NULL,
    provider          TEXT NOT NULL,
    redirect_uri      TEXT NOT NULL,
    verifier_envelope TEXT NOT NULL,
    expires_at        TEXT NOT NULL,
    consumed_at       TEXT,
    created_at        TEXT NOT NULL
) STRICT;

CREATE TABLE core_credential_audit (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    credential_id TEXT    NOT NULL,
    actor         TEXT    NOT NULL,
    action        TEXT    NOT NULL,
    version       INTEGER NOT NULL,
    created_at    TEXT    NOT NULL
) STRICT;

CREATE TABLE core_credential_references (
    owner         TEXT NOT NULL,
    credential_id TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    PRIMARY KEY (owner, credential_id)
) STRICT;

CREATE INDEX core_credential_references_id ON core_credential_references(credential_id);
`},
}
