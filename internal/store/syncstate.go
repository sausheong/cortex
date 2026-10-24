package store

import (
	"context"
	"database/sql"
	"fmt"
)

// GetSyncState returns the last-known sync state for a connector. For an
// encrypted user the state is sealed (connector cursors can name content).
func (h *Handle) GetSyncState(ctx context.Context, connector string) (string, error) {
	if err := h.RequireKey(); err != nil {
		return "", err
	}
	var state sql.NullString
	var ct []byte
	var enc bool
	err := h.DB.QueryRowContext(ctx,
		"SELECT state, state_ct, enc FROM sync_state WHERE user_id = ? AND connector = ?", h.UserID, connector,
	).Scan(&state, &ct, &enc)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("cortex: get sync state: %w", err)
	}
	if err := h.CheckRowForm(enc); err != nil {
		return "", err
	}
	if enc {
		return h.OpenStr("sync_state", connector, "state", ct)
	}
	return state.String, nil
}

// SetSyncState upserts the sync state for a connector, sealed for an
// encrypted user.
func (h *Handle) SetSyncState(ctx context.Context, connector, state string) error {
	if err := h.RequireKey(); err != nil {
		return err
	}
	var plain any = state
	var ct []byte
	enc := h.Encrypted()
	if enc {
		var err error
		if ct, err = h.SealStr("sync_state", connector, "state", state); err != nil {
			return err
		}
		plain = nil
	}
	_, err := h.DB.ExecContext(ctx,
		`INSERT INTO sync_state (user_id, connector, state, state_ct, enc, updated_at)
		 VALUES (?, ?, ?, ?, ?, now())
		 ON CONFLICT(user_id, connector) DO UPDATE SET state = excluded.state, state_ct = excluded.state_ct,
		   enc = excluded.enc, updated_at = excluded.updated_at`,
		h.UserID, connector, plain, ct, enc,
	)
	if err != nil {
		return fmt.Errorf("cortex: set sync state: %w", err)
	}
	h.NotifyWrite("sync_state")
	return nil
}
