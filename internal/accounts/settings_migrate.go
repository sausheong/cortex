package accounts

import (
	"context"
	"database/sql"
	"errors"

	"github.com/sausheong/cortex/internal/keys"
)

// SealLegacySettingsTx re-seals a legacy user_settings row (fields sealed
// under CORTEX_SECRET_KEY) into the user-key form, inside tx. It reports
// whether a legacy row was converted; an absent or already encrypted row is a
// no-op. The row is locked for the duration.
func (st *Store) SealLegacySettingsTx(ctx context.Context, tx *sql.Tx, userID string, uk *keys.UK) (bool, error) {
	r, err := loadRow(ctx, tx, userID, true)
	if errors.Is(err, ErrNoSettings) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if r.enc {
		return false, nil
	}
	s, active, err := st.decode(userID, r, nil)
	if err != nil { // unreadable active embedder: settings still convert
		if s, err = st.legacySettings(r); err != nil {
			return false, err
		}
		active = nil
	}
	if err := writeEncrypted(ctx, tx, userID, uk, *s, active, st.now()); err != nil {
		return false, err
	}
	return true, nil
}
