// Package alert delivers issue notifications from the store's outbox to
// Slack and Linear, keeps Linear issues in sync, and seals destination
// credentials at rest.
package alert

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ErrNoSecretKey means alert targets cannot be stored or read because
// WATCHTOWER_SECRET_KEY is unset.
var ErrNoSecretKey = errors.New("WATCHTOWER_SECRET_KEY is not set; it is required to store alert webhooks")

// Sealer encrypts alert targets (webhook URLs) at rest with AES-256-GCM.
type Sealer struct {
	aead  cipher.AEAD
	idKey []byte // derives stable identifiers; separate from the encryption key
}

// NewSealer parses a 32-byte key given as 64 hex characters or base64.
// An empty key returns a nil Sealer, whose methods report ErrNoSecretKey.
func NewSealer(key string) (*Sealer, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, nil
	}
	raw, err := hex.DecodeString(key)
	if err != nil {
		raw, err = base64.StdEncoding.DecodeString(key)
	}
	if err != nil || len(raw) != 32 {
		return nil, fmt.Errorf("WATCHTOWER_SECRET_KEY must be 32 bytes as hex or base64 (e.g. `openssl rand -hex 32`)")
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, raw)
	mac.Write([]byte("watchtower identifier derivation"))
	return &Sealer{aead: aead, idKey: mac.Sum(nil)}, nil
}

func (s *Sealer) Seal(plaintext string) ([]byte, error) {
	if s == nil {
		return nil, ErrNoSecretKey
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

func (s *Sealer) Open(sealed []byte) (string, error) {
	if s == nil {
		return "", ErrNoSecretKey
	}
	n := s.aead.NonceSize()
	if len(sealed) < n {
		return "", errors.New("sealed value too short")
	}
	plain, err := s.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return "", errors.New("cannot decrypt alert target; was WATCHTOWER_SECRET_KEY changed?")
	}
	return string(plain), nil
}

// DeriveUUID returns a version 4 UUID that is stable for label on this
// installation and unpredictable without its secret key. It lets an
// external system deduplicate retried creations.
func (s *Sealer) DeriveUUID(label string) (string, error) {
	if s == nil {
		return "", ErrNoSecretKey
	}
	mac := hmac.New(sha256.New, s.idKey)
	mac.Write([]byte(label))
	b := mac.Sum(nil)[:16]
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
