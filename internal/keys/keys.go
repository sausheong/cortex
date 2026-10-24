// Package keys holds the crypto primitives for per-user encryption.
//
// A user key (UK) is 32 random bytes. A data key and a blind-index key are
// derived from it with HKDF-SHA256. Rows are sealed with AES-256-GCM as
// v1 | nonce | ciphertext, with a length-prefixed AAD binding user, table, row
// and column.
//
// Nonce bound: nonces are random 96-bit values, which is safe below about
// 2^32 messages per data key. Embeddings are sealed once per generation, so
// volume stays far below that bound.
package keys

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"sync"

	"golang.org/x/crypto/hkdf"
)

var (
	ErrDecrypt = errors.New("keys: cannot decrypt")
	ErrFormat  = errors.New("keys: bad format")
)

const (
	versionV1 = 0x01
	nonceLen  = 12
	tagLen    = 16
	ukLen     = 32
	blindLen  = 16
)

// UK is a per-user key. It is safe for concurrent use; Zero makes it unusable.
type UK struct {
	mu       sync.RWMutex
	key      [ukLen]byte
	dataKey  []byte
	blindKey []byte
	dead     bool
}

func hkdfKey(secret []byte, info string) []byte {
	out := make([]byte, 32)
	r := hkdf.New(sha256.New, secret, nil, []byte(info))
	if _, err := io.ReadFull(r, out); err != nil {
		panic("keys: hkdf: " + err.Error())
	}
	return out
}

func newFrom(b []byte) *UK {
	u := &UK{}
	copy(u.key[:], b)
	u.dataKey = hkdfKey(u.key[:], "cortex/data/v1")
	u.blindKey = hkdfKey(u.key[:], "cortex/blind/v1")
	return u
}

// NewUK generates a fresh random user key.
func NewUK() (*UK, error) {
	b := make([]byte, ukLen)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	u := newFrom(b)
	clear(b)
	return u, nil
}

// FromBytes builds a UK from exactly 32 bytes (the input is copied).
func FromBytes(b []byte) (*UK, error) {
	if len(b) != ukLen {
		return nil, ErrFormat
	}
	return newFrom(b), nil
}

// Bytes returns a copy of the raw key, or nil after Zero.
func (u *UK) Bytes() []byte {
	u.mu.RLock()
	defer u.mu.RUnlock()
	if u.dead {
		return nil
	}
	return append([]byte(nil), u.key[:]...)
}

// Clone returns an independent copy, or nil after Zero.
func (u *UK) Clone() *UK {
	u.mu.RLock()
	defer u.mu.RUnlock()
	if u.dead {
		return nil
	}
	return newFrom(u.key[:])
}

// Zero wipes the key and every cached derived key. Later calls fail.
func (u *UK) Zero() {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	clear(u.key[:])
	clear(u.dataKey)
	clear(u.blindKey)
	u.dead = true
}

// aad length-prefixes every component (uvarint length plus bytes).
func aad(parts ...string) []byte {
	var out []byte
	for _, p := range parts {
		out = binary.AppendUvarint(out, uint64(len(p)))
		out = append(out, p...)
	}
	return out
}

func gcm(key []byte) (cipher.AEAD, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

func seal(key, ad, plain []byte) ([]byte, error) {
	g, err := gcm(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 1+nonceLen, 1+nonceLen+len(plain)+tagLen)
	out[0] = versionV1
	if _, err := rand.Read(out[1 : 1+nonceLen]); err != nil {
		return nil, err
	}
	return g.Seal(out, out[1:1+nonceLen], plain, ad), nil
}

func open(key, ad, ct []byte) ([]byte, error) {
	if len(ct) < 1+nonceLen+tagLen || ct[0] != versionV1 {
		return nil, ErrDecrypt
	}
	g, err := gcm(key)
	if err != nil {
		return nil, ErrDecrypt
	}
	pt, err := g.Open(nil, ct[1:1+nonceLen], ct[1+nonceLen:], ad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

// Seal encrypts plain bound to (userID, table, rowID, column).
func (u *UK) Seal(userID, table, rowID, column string, plain []byte) ([]byte, error) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	if u.dead {
		return nil, errors.New("keys: key zeroed")
	}
	return seal(u.dataKey, aad("cortex/row/v1", userID, table, rowID, column), plain)
}

// Open decrypts a Seal output; any mismatch yields ErrDecrypt.
func (u *UK) Open(userID, table, rowID, column string, ct []byte) ([]byte, error) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	if u.dead {
		return nil, errors.New("keys: key zeroed")
	}
	return open(u.dataKey, aad("cortex/row/v1", userID, table, rowID, column), ct)
}

// Blind returns a 16-byte keyed index of a normalised value, nil after Zero.
func (u *UK) Blind(norm string) []byte {
	u.mu.RLock()
	defer u.mu.RUnlock()
	if u.dead {
		return nil
	}
	m := hmac.New(sha256.New, u.blindKey)
	m.Write([]byte(norm))
	return m.Sum(nil)[:blindLen]
}
