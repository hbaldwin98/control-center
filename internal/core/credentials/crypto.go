package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
)

const envelopeFormat = 1

// envelope is the AES-256-GCM wrapping stored for every secret. Plaintext never lives
// in SQLite, logs, events, or Admin responses.
type envelope struct {
	FormatVersion int    `json:"format_version"`
	KeyVersion    int    `json:"key_version"`
	Nonce         []byte `json:"nonce"`
	Ciphertext    []byte `json:"ciphertext"`
}

func (s *Store) encrypt(plain []byte, table, id, kind, provider string, credVersion int64) (string, error) {
	key, ok := s.keys[s.active]
	if !ok || len(key) != 32 {
		return "", ErrMasterKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("credentials: nonce: %w", err)
	}
	aad := bindAAD(table, id, kind, provider, credVersion, s.active)
	ct := gcm.Seal(nil, nonce, plain, aad)
	raw, err := json.Marshal(envelope{
		FormatVersion: envelopeFormat,
		KeyVersion:    s.active,
		Nonce:         nonce,
		Ciphertext:    ct,
	})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (s *Store) decrypt(raw, table, id, kind, provider string, credVersion int64) ([]byte, error) {
	var env envelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEnvelope, err)
	}
	if env.FormatVersion != envelopeFormat {
		return nil, fmt.Errorf("%w: unknown format %d", ErrEnvelope, env.FormatVersion)
	}
	key, ok := s.keys[env.KeyVersion]
	if !ok || len(key) != 32 {
		return nil, fmt.Errorf("%w: missing key version %d", ErrMasterKey, env.KeyVersion)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	aad := bindAAD(table, id, kind, provider, credVersion, env.KeyVersion)
	plain, err := gcm.Open(nil, env.Nonce, env.Ciphertext, aad)
	if err != nil {
		return nil, ErrEnvelope
	}
	return plain, nil
}

// bindAAD is a canonical length-prefixed encoding so a ciphertext cannot be moved to
// another row, table, kind, provider, credential version, or key version.
func bindAAD(table, id, kind, provider string, credVersion int64, keyVersion int) []byte {
	var b []byte
	b = appendLP(b, table)
	b = appendLP(b, id)
	b = appendLP(b, kind)
	b = appendLP(b, provider)
	var nums [16]byte
	binary.BigEndian.PutUint64(nums[0:8], uint64(credVersion))
	binary.BigEndian.PutUint64(nums[8:16], uint64(keyVersion))
	return append(b, nums[:]...)
}

func appendLP(b []byte, s string) []byte {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(s)))
	b = append(b, n[:]...)
	return append(b, s...)
}
