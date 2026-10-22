// Package accounts holds per-user AI settings, connections, tokens, crypto
// and the hardened provider HTTP client.
package accounts

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
)

// Sealer encrypts secrets with AES-256-GCM.
type Sealer struct{ aead cipher.AEAD }

// NewSealer builds a Sealer from a 32-byte key given as 64 hex characters.
func NewSealer(hexKey string) (*Sealer, error) {
	key, err := hex.DecodeString(hexKey)
	if err != nil || len(key) != 32 {
		return nil, errors.New("accounts: CORTEX_SECRET_KEY must be 32 bytes as 64 hex characters")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

// Seal encrypts plain with a random nonce and returns base64(nonce||ciphertext).
func (s *Sealer) Seal(plain string) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("accounts: nonce: %w", err)
	}
	out := s.aead.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(out), nil
}

// Open decrypts a value produced by Seal.
func (s *Sealer) Open(sealed string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", errors.New("accounts: bad sealed value")
	}
	ns := s.aead.NonceSize()
	if len(raw) < ns+s.aead.Overhead() {
		return "", errors.New("accounts: bad sealed value")
	}
	plain, err := s.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", errors.New("accounts: cannot decrypt (wrong key?)")
	}
	return string(plain), nil
}
