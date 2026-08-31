package credentials

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hbaldwin98/control-center/internal/core/storage"
)

func (s *Store) List(ctx context.Context) ([]Credential, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, kind, provider, status, version, expires_at, scopes, secret_envelope
		   FROM core_credentials ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Credential
	for rows.Next() {
		r, err := scanCred(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r.public())
	}
	if out == nil {
		out = []Credential{}
	}
	return out, rows.Err()
}

func (s *Store) References(ctx context.Context, id string) ([]string, error) {
	if _, err := s.read(ctx, id); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx,
		`SELECT owner FROM core_credential_references WHERE credential_id = ? ORDER BY owner`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var owners []string
	for rows.Next() {
		var o string
		if err := rows.Scan(&o); err != nil {
			return nil, err
		}
		owners = append(owners, o)
	}
	if owners == nil {
		owners = []string{}
	}
	return owners, rows.Err()
}

func (s *Store) Delete(ctx context.Context, id string) error {
	actor, err := actor(ctx)
	if err != nil {
		return err
	}
	return s.db.Tx(ctx, func(tx storage.Tx) error {
		if _, err := readTx(ctx, tx, id); err != nil {
			return err
		}
		rows, err := tx.Query(ctx,
			`SELECT owner FROM core_credential_references WHERE credential_id = ? ORDER BY owner`, id)
		if err != nil {
			return err
		}
		var owners []string
		for rows.Next() {
			var o string
			if err := rows.Scan(&o); err != nil {
				rows.Close()
				return err
			}
			owners = append(owners, o)
		}
		rows.Close()
		if len(owners) > 0 {
			return fmt.Errorf("%w: %v", ErrCredentialInUse, owners)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM core_credentials WHERE id = ?`, id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO core_credential_audit(credential_id, actor, action, version, created_at)
			 VALUES (?, ?, 'delete', 0, ?)`, id, actor, rfc(s.now()))
		return err
	})
}

func (s *Store) CreateAPIKey(ctx context.Context, in APIKeyInput) (Credential, error) {
	actor, err := actor(ctx)
	if err != nil {
		return Credential{}, err
	}
	if !validID(in.ID) {
		return Credential{}, ErrInvalidID
	}
	if in.Provider == "" || !validID(in.Provider) {
		return Credential{}, fmt.Errorf("%w: provider", ErrInvalidID)
	}
	if in.Secret.Value == "" {
		return Credential{}, ErrInvalidSecret
	}
	env, err := s.encrypt([]byte(in.Secret.Value), "core_credentials", in.ID, string(KindAPIKey), in.Provider, 1)
	if err != nil {
		return Credential{}, err
	}
	now := rfc(s.now())
	err = s.db.Tx(ctx, func(tx storage.Tx) error {
		var exists string
		err := tx.QueryRow(ctx, `SELECT id FROM core_credentials WHERE id = ?`, in.ID).Scan(&exists)
		if err == nil {
			return ErrDuplicateID
		}
		if !storage.IsNoRows(err) {
			return err
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO core_credentials(id, kind, provider, status, version, expires_at, scopes, secret_envelope, created_at, updated_at)
			 VALUES (?, ?, ?, ?, 1, NULL, '[]', ?, ?, ?)`,
			in.ID, string(KindAPIKey), in.Provider, string(StatusOK), env, now, now)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO core_credential_audit(credential_id, actor, action, version, created_at)
			 VALUES (?, ?, 'create', 1, ?)`, in.ID, actor, now)
		return err
	})
	if err != nil {
		return Credential{}, err
	}
	return Credential{ID: in.ID, Kind: KindAPIKey, Provider: in.Provider, Status: StatusOK, Version: 1, Scopes: []string{}}, nil
}

func (s *Store) ReplaceAPIKey(ctx context.Context, id string, secret SecretInput) (Credential, error) {
	return s.replaceAPIKey(ctx, id, secret, false)
}

func (s *Store) RotateAPIKey(ctx context.Context, id string, next SecretInput) (Credential, error) {
	return s.replaceAPIKey(ctx, id, next, true)
}

func (s *Store) replaceAPIKey(ctx context.Context, id string, secret SecretInput, rotate bool) (Credential, error) {
	actor, err := actor(ctx)
	if err != nil {
		return Credential{}, err
	}
	if secret.Value == "" {
		return Credential{}, ErrInvalidSecret
	}
	var out Credential
	err = s.db.Tx(ctx, func(tx storage.Tx) error {
		row, err := readTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if row.kind != KindAPIKey {
			return ErrKindMismatch
		}
		next := row.version + 1
		env, err := s.encrypt([]byte(secret.Value), "core_credentials", id, string(KindAPIKey), row.provider, next)
		if err != nil {
			return err
		}
		now := rfc(s.now())
		if _, err := tx.Exec(ctx,
			`UPDATE core_credentials SET secret_envelope = ?, version = ?, status = ?, updated_at = ? WHERE id = ?`,
			env, next, string(StatusOK), now, id); err != nil {
			return err
		}
		action := "replace"
		if rotate {
			action = "rotate"
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO core_credential_audit(credential_id, actor, action, version, created_at)
			 VALUES (?, ?, ?, ?, ?)`, id, actor, action, next, now); err != nil {
			return err
		}
		out = Credential{ID: id, Kind: KindAPIKey, Provider: row.provider, Status: StatusOK, Version: next, Scopes: row.scopes}
		return nil
	})
	return out, err
}

func (s *Store) audit(ctx context.Context, tx storage.Tx, id, actor, action string, version int64) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO core_credential_audit(credential_id, actor, action, version, created_at)
		 VALUES (?, ?, ?, ?, ?)`, id, actor, action, version, rfc(s.now()))
	return err
}

func marshalScopes(s []string) string {
	if s == nil {
		s = []string{}
	}
	b, _ := json.Marshal(s)
	return string(b)
}
