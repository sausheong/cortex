package store

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/sausheong/cortex/internal/model"
	"golang.org/x/text/unicode/norm"
)

// ErrCiphertextMissing is returned when a row flagged enc=true has a NULL or
// empty ciphertext in a column that must hold one. It never carries content.
var ErrCiphertextMissing = errors.New("cortex: ciphertext missing from an encrypted row")

// OnWrite is called after a content write (kind is "entity", "relationship",
// "alias", "memory", "chunk", "embedding" or "memory_edge"). The working set
// (Task 11) sets it to invalidate its per-user cache. It may be nil.
var OnWrite func(userID, kind string)

// NotifyWrite reports a content write for this handle's user.
func (h *Handle) NotifyWrite(kind string) {
	if f := OnWrite; f != nil {
		f(h.UserID, kind)
	}
}

// NormName is the comparison key for names, types and sources: NFC, trimmed,
// lower-cased. It matches the semantics of lower(name) used by the legacy
// lookups and is the input of every blind index.
func NormName(s string) string {
	return norm.NFC.String(strings.ToLower(strings.TrimSpace(norm.NFC.String(s))))
}

// Migrating reports whether the user is mid-migration, the only state in
// which legacy plaintext rows are still looked up and rewritten.
func (h *Handle) Migrating() bool { return h.EncState == "migrating" }

// RequireKey returns ErrLocked when the user is encrypted but this handle
// carries no key. Unencrypted handles pass.
func (h *Handle) RequireKey() error {
	if h.Encrypted() && h.Key == nil {
		return model.ErrLocked
	}
	return nil
}

// CheckRowForm is the row-form guard: for a user in state "encrypted" a row
// with enc=false is rejected. Legacy rows are accepted only in "none" and
// "migrating".
func (h *Handle) CheckRowForm(rowEnc bool) error {
	if !rowEnc && h.EncState == "encrypted" {
		return model.ErrPlaintextRow
	}
	return nil
}

// BlindNorm returns the blind index of NormName(s), nil when keyless.
func (h *Handle) BlindNorm(s string) []byte { return h.Blind(NormName(s)) }

// SealStr seals a string.
func (h *Handle) SealStr(table, rowID, col, s string) ([]byte, error) {
	return h.Seal(table, rowID, col, []byte(s))
}

// OpenStr opens a required string column of an enc=true row.
func (h *Handle) OpenStr(table, rowID, col string, ct []byte) (string, error) {
	if len(ct) == 0 {
		return "", ErrCiphertextMissing
	}
	b, err := h.Open(table, rowID, col, ct)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// OpenOptStr opens an optional string column: NULL yields "".
func (h *Handle) OpenOptStr(table, rowID, col string, ct []byte) (string, error) {
	if len(ct) == 0 {
		return "", nil
	}
	return h.OpenStr(table, rowID, col, ct)
}

// SealJSON seals the JSON encoding of v.
func (h *Handle) SealJSON(table, rowID, col string, v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("cortex: marshal %s: %w", col, err)
	}
	return h.Seal(table, rowID, col, b)
}

// OpenJSON opens a required JSON column into out.
func (h *Handle) OpenJSON(table, rowID, col string, ct []byte, out any) error {
	if len(ct) == 0 {
		return ErrCiphertextMissing
	}
	b, err := h.Open(table, rowID, col, ct)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("cortex: decode %s: %w", col, err)
	}
	return nil
}

// EncodeVec encodes a vector as little-endian float32 bytes.
func EncodeVec(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

// DecodeVec is the inverse of EncodeVec.
func DecodeVec(b []byte) ([]float32, error) {
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("cortex: bad vector length %d", len(b))
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v, nil
}

// IsUniqueViolation reports a Postgres unique-constraint violation.
func IsUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}
