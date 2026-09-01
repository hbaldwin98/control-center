package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/hbaldwin98/control-center/internal/core/storage"
)

// There is one administrator principal and no user-management surface.
const adminRowID = 1

var (
	errNoAdmin        = errors.New("web: no administrator configured")
	errBadCredentials = errors.New("web: invalid credentials")
	errBadToken       = errors.New("web: invalid bootstrap token")
)

// argon2idParams are the password hashing parameters. They are stored in the encoded hash
// so they can be raised later without invalidating existing passwords.
type argon2idParams struct {
	Time    uint32
	Memory  uint32 // KiB
	Threads uint8
	KeyLen  uint32
}

var defaultArgon2id = argon2idParams{Time: 3, Memory: 64 * 1024, Threads: 4, KeyLen: 32}

func hashPassword(password string, p argon2idParams) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// verifyPassword is constant time with respect to the stored key.
func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var mem, tim, thr int
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &tim, &thr); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt,
		uint32(tim), uint32(mem), uint8(thr), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// authStore owns the administrator row, the bootstrap token, and sessions.
type authStore struct {
	db  storage.DB
	now func() time.Time
}

func (a *authStore) adminHash(ctx context.Context) (string, error) {
	var h string
	err := a.db.QueryRow(ctx, `SELECT password_hash FROM core_admin WHERE id = ?`, adminRowID).Scan(&h)
	if storage.IsNoRows(err) {
		return "", errNoAdmin
	}
	return h, err
}

// adminExists reports whether first-run bootstrap has already completed.
func (a *authStore) adminExists(ctx context.Context) (bool, error) {
	_, err := a.adminHash(ctx)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, errNoAdmin):
		return false, nil
	default:
		return false, err
	}
}

// ensureBootstrapToken creates the one-time token when no administrator exists yet and
// returns it in clear text exactly once, for the operator to read from the server log.
// It returns an empty string when an administrator is already configured. A restart
// before bootstrap issues a fresh token and invalidates the previous one.
func (a *authStore) ensureBootstrapToken(ctx context.Context) (string, error) {
	exists, err := a.adminExists(ctx)
	if err != nil || exists {
		return "", err
	}
	token := randomToken(32)
	sum := sha256.Sum256([]byte(token))
	now := a.now().UTC().Format(time.RFC3339Nano)
	_, err = a.db.Exec(ctx,
		`INSERT INTO core_bootstrap_token(id, token_hash, created_at, used_at)
		 VALUES (?, ?, ?, NULL)
		 ON CONFLICT(id) DO UPDATE SET token_hash = excluded.token_hash,
		                               created_at = excluded.created_at,
		                               used_at    = NULL`,
		adminRowID, sum[:], now)
	if err != nil {
		return "", err
	}
	return token, nil
}

// bootstrap consumes the one-time token and sets the administrator password. It is
// rejected once an administrator exists.
func (a *authStore) bootstrap(ctx context.Context, token, password string) error {
	sum := sha256.Sum256([]byte(token))
	hash, err := hashPassword(password, defaultArgon2id)
	if err != nil {
		return err
	}
	now := a.now().UTC().Format(time.RFC3339Nano)

	return a.db.Tx(ctx, func(tx storage.Tx) error {
		var exists int
		err := tx.QueryRow(ctx, `SELECT 1 FROM core_admin WHERE id = ?`, adminRowID).Scan(&exists)
		if err == nil {
			return errBadToken
		}
		if !storage.IsNoRows(err) {
			return err
		}

		var stored []byte
		var usedAt *string
		err = tx.QueryRow(ctx,
			`SELECT token_hash, used_at FROM core_bootstrap_token WHERE id = ?`, adminRowID).
			Scan(&stored, &usedAt)
		if storage.IsNoRows(err) {
			return errBadToken
		}
		if err != nil {
			return err
		}
		if usedAt != nil || subtle.ConstantTimeCompare(stored, sum[:]) != 1 {
			return errBadToken
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO core_admin(id, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?)`,
			adminRowID, hash, now, now); err != nil {
			return err
		}
		_, err = tx.Exec(ctx,
			`UPDATE core_bootstrap_token SET used_at = ? WHERE id = ?`, now, adminRowID)
		return err
	})
}

// createAdminIfAbsent sets the administrator password when none exists. It is the
// container first-run path: the peer is not loopback, so the one-time token cannot be
// posted from the browser. Returns true when a row was inserted.
func (a *authStore) createAdminIfAbsent(ctx context.Context, password string) (bool, error) {
	if len(password) < minPasswordLen {
		return false, fmt.Errorf("web: bootstrap password must be at least %d characters", minPasswordLen)
	}
	exists, err := a.adminExists(ctx)
	if err != nil || exists {
		return false, err
	}
	hash, err := hashPassword(password, defaultArgon2id)
	if err != nil {
		return false, err
	}
	now := a.now().UTC().Format(time.RFC3339Nano)
	err = a.db.Tx(ctx, func(tx storage.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO core_admin(id, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?)`,
			adminRowID, hash, now, now); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`UPDATE core_bootstrap_token SET used_at = ? WHERE id = ? AND used_at IS NULL`, now, adminRowID)
		return err
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

func (a *authStore) checkPassword(ctx context.Context, password string) error {
	hash, err := a.adminHash(ctx)
	if err != nil {
		return err
	}
	if !verifyPassword(password, hash) {
		return errBadCredentials
	}
	return nil
}

// session is a live administrator session.
type session struct {
	ID        string
	ExpiresAt time.Time
	ReauthAt  *time.Time
}

// reauthFresh reports whether the session reauthenticated within the window. Credential
// changes require this.
func (s *session) reauthFresh(now time.Time, window time.Duration) bool {
	return s.ReauthAt != nil && now.Sub(*s.ReauthAt) <= window
}

// create issues a new session and its bound CSRF token. The returned cookie value is
// "<id>.<secret>"; only the secret's digest is persisted.
func (a *authStore) create(ctx context.Context, absolute time.Duration) (cookie, csrf string, err error) {
	id := randomToken(16)
	secret := randomToken(32)
	tokenSum := sha256.Sum256([]byte(secret))
	now := a.now().UTC()

	_, err = a.db.Exec(ctx,
		`INSERT INTO core_sessions(id, token_hash, created_at, last_seen_at, expires_at, reauth_at)
		 VALUES (?, ?, ?, ?, ?, NULL)`,
		id, tokenSum[:],
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		now.Add(absolute).Format(time.RFC3339Nano))
	if err != nil {
		return "", "", err
	}
	return id + "." + secret, deriveCSRF(tokenSum[:], id), nil
}

// lookup validates a session cookie against the absolute and idle policies and refreshes
// last_seen_at. An expired session is deleted.
func (a *authStore) lookup(ctx context.Context, cookie string, idle time.Duration) (*session, error) {
	id, secret, ok := strings.Cut(cookie, ".")
	if !ok {
		return nil, errBadCredentials
	}
	sum := sha256.Sum256([]byte(secret))

	var storedToken []byte
	var lastSeen, expires string
	var reauth *string
	err := a.db.QueryRow(ctx,
		`SELECT token_hash, last_seen_at, expires_at, reauth_at FROM core_sessions WHERE id = ?`, id).
		Scan(&storedToken, &lastSeen, &expires, &reauth)
	if storage.IsNoRows(err) {
		return nil, errBadCredentials
	}
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare(storedToken, sum[:]) != 1 {
		return nil, errBadCredentials
	}

	now := a.now().UTC()
	expiresAt, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil {
		return nil, err
	}
	lastSeenAt, err := time.Parse(time.RFC3339Nano, lastSeen)
	if err != nil {
		return nil, err
	}
	if now.After(expiresAt) || now.Sub(lastSeenAt) > idle {
		_ = a.delete(ctx, id)
		return nil, errBadCredentials
	}

	if _, err := a.db.Exec(ctx,
		`UPDATE core_sessions SET last_seen_at = ? WHERE id = ?`,
		now.Format(time.RFC3339Nano), id); err != nil {
		return nil, err
	}

	s := &session{ID: id, ExpiresAt: expiresAt}
	if reauth != nil {
		if t, err := time.Parse(time.RFC3339Nano, *reauth); err == nil {
			s.ReauthAt = &t
		}
	}
	return s, nil
}

// deriveCSRF computes the session's synchronizer token as HMAC(token_hash, sessionID).
// Deriving rather than storing keeps the token stable for the life of the session, so
// every tab that bootstraps the same session gets the same token instead of retiring the
// one its siblings are still holding. The key is the digest of the cookie secret: unique
// per session, never sent to a client, and gone the moment the session is deleted.
func deriveCSRF(tokenHash []byte, sessionID string) string {
	mac := hmac.New(sha256.New, tokenHash)
	mac.Write([]byte(sessionID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// sessionTokenHash reads the stored digest of a session's cookie secret, the key the
// synchronizer token is derived from.
func (a *authStore) sessionTokenHash(ctx context.Context, sessionID string) ([]byte, error) {
	var stored []byte
	if err := a.db.QueryRow(ctx,
		`SELECT token_hash FROM core_sessions WHERE id = ?`, sessionID).Scan(&stored); err != nil {
		return nil, err
	}
	return stored, nil
}

// checkCSRF compares a submitted synchronizer token against the one bound to the session.
func (a *authStore) checkCSRF(ctx context.Context, sessionID, token string) bool {
	if token == "" {
		return false
	}
	tokenHash, err := a.sessionTokenHash(ctx, sessionID)
	if err != nil {
		return false
	}
	want := deriveCSRF(tokenHash, sessionID)
	return subtle.ConstantTimeCompare([]byte(want), []byte(token)) == 1
}

// csrfToken returns the session's synchronizer token. /api/bootstrap returns it so the
// shell can send mutations after a page reload without logging in again.
func (a *authStore) csrfToken(ctx context.Context, sessionID string) (string, error) {
	tokenHash, err := a.sessionTokenHash(ctx, sessionID)
	if err != nil {
		return "", err
	}
	return deriveCSRF(tokenHash, sessionID), nil
}

func (a *authStore) markReauth(ctx context.Context, sessionID string) error {
	now := a.now().UTC().Format(time.RFC3339Nano)
	_, err := a.db.Exec(ctx, `UPDATE core_sessions SET reauth_at = ? WHERE id = ?`, now, sessionID)
	return err
}

func (a *authStore) delete(ctx context.Context, sessionID string) error {
	_, err := a.db.Exec(ctx, `DELETE FROM core_sessions WHERE id = ?`, sessionID)
	return err
}

// purgeExpired removes sessions past their absolute lifetime.
func (a *authStore) purgeExpired(ctx context.Context) error {
	_, err := a.db.Exec(ctx, `DELETE FROM core_sessions WHERE expires_at < ?`,
		a.now().UTC().Format(time.RFC3339Nano))
	return err
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("web: crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// formatID renders an int64 event ID as the decimal string the wire format uses.
func formatID(id int64) string { return strconv.FormatInt(id, 10) }

// setPassword replaces the administrator password. It is rejected before bootstrap, so
// there is no path that creates the admin row outside of the one-time token flow.
func (a *authStore) setPassword(ctx context.Context, password string) error {
	hash, err := hashPassword(password, defaultArgon2id)
	if err != nil {
		return err
	}
	now := a.now().UTC().Format(time.RFC3339Nano)
	res, err := a.db.Exec(ctx,
		`UPDATE core_admin SET password_hash = ?, updated_at = ? WHERE id = ?`,
		hash, now, adminRowID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errNoAdmin
	}
	return nil
}

// deleteOthers signs out every session but the one that changed the password, so a
// password change also revokes sessions on devices the administrator no longer holds.
func (a *authStore) deleteOthers(ctx context.Context, keepID string) error {
	_, err := a.db.Exec(ctx, `DELETE FROM core_sessions WHERE id <> ?`, keepID)
	return err
}
