package keys

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"strings"
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func encodeRecovery(raw []byte) string {
	s := b32.EncodeToString(raw)
	var parts []string
	for len(s) > 4 {
		parts = append(parts, s[:4])
		s = s[4:]
	}
	if s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, "-")
}

// NewRecoveryKey returns 128 random bits and a display form (base32, groups
// of four separated by dashes).
func NewRecoveryKey() (raw []byte, display string, err error) {
	raw = make([]byte, 16)
	if _, err = rand.Read(raw); err != nil {
		return nil, "", err
	}
	return raw, encodeRecovery(raw), nil
}

// ParseRecoveryKey accepts any case and ignores spaces and dashes.
func ParseRecoveryKey(s string) ([]byte, error) {
	s = strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "\t", "", "\n", "").Replace(s))
	raw, err := b32.DecodeString(s)
	if err != nil || len(raw) != 16 {
		return nil, errors.New("keys: invalid recovery key")
	}
	return raw, nil
}
