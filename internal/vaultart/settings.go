// Package vaultart decides which entities and topics get a Vault article and
// carries the per-user settings that govern it. It is pure over a decrypted
// workset; nothing here touches the network or holds a key.
package vaultart

import (
	"context"
	"errors"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
)

// Article length words.
const (
	LengthShort    = "short"
	LengthStandard = "standard"
	LengthLong     = "long"
)

// Threshold bounds.
const (
	MinThreshold     = 1
	MaxThreshold     = 50
	DefaultThreshold = 3
)

// Settings are the user's Vault article preferences. They are stored sealed
// in vault_settings, apart from the provider settings, so saving one never
// touches the other.
type Settings struct {
	Threshold        int    `json:"threshold"`
	IncludeConnected bool   `json:"include_connected"`
	Length           string `json:"length"`
}

// DefaultSettings returns the defaults: 3 live memories, connected entities
// included, standard length.
func DefaultSettings() Settings {
	return Settings{Threshold: DefaultThreshold, IncludeConnected: true, Length: LengthStandard}
}

// Normalize clamps the threshold to 1..50 and the length to a known word.
func (s Settings) Normalize() Settings {
	if s.Threshold < MinThreshold {
		s.Threshold = MinThreshold
	}
	if s.Threshold > MaxThreshold {
		s.Threshold = MaxThreshold
	}
	switch s.Length {
	case LengthShort, LengthStandard, LengthLong:
	default:
		s.Length = LengthStandard
	}
	return s
}

// LoadSettings reads the user's settings; absent means the defaults.
func LoadSettings(ctx context.Context, h *store.Handle) (Settings, error) {
	var s Settings
	err := h.GetVaultSettings(ctx, &s)
	if errors.Is(err, model.ErrNotFound) {
		return DefaultSettings(), nil
	}
	if err != nil {
		return Settings{}, err
	}
	return s.Normalize(), nil
}

// SaveSettings normalises and stores the settings.
func SaveSettings(ctx context.Context, h *store.Handle, s Settings) error {
	return h.PutVaultSettings(ctx, s.Normalize())
}
