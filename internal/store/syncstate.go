package store

import (
	"context"
	"database/sql"
	"fmt"
)

// GetSyncState returns the last-known sync state for a connector. The state
// is sealed (connector cursors can name content).
func (h *Handle) GetSyncState(ctx context.Context, connector string) (string, error) {
	if err := h.RequireKey(); err != nil {
		return "", err
	}
	var ct []byte
	err := h.DB.QueryRowContext(ctx,
		"SELECT state_ct FROM sync_state WHERE user_id = ? AND connector = ?", h.UserID, connector,
	).Scan(&ct)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("cortex: get sync state: %w", err)
	}
	return h.OpenStr("sync_state", connector, "state", ct)
}

// SetSyncState upserts the sealed sync state for a connector.
func (h *Handle) SetSyncState(ctx context.Context, connector, state string) error {
	if err := h.RequireKey(); err != nil {
		return err
	}
	ct, err := h.SealStr("sync_state", connector, "state", state)
	if err != nil {
		return err
	}
	_, err = h.DB.ExecContext(ctx,
		`INSERT INTO sync_state (user_id, connector, state_ct, updated_at)
		 VALUES (?, ?, ?, now())
		 ON CONFLICT(user_id, connector) DO UPDATE SET state_ct = excluded.state_ct,
		   updated_at = excluded.updated_at`,
		h.UserID, connector, ct,
	)
	if err != nil {
		return fmt.Errorf("cortex: set sync state: %w", err)
	}
	h.NotifyWrite("sync_state")
	return nil
}
