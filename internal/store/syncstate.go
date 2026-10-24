package store

import (
	"context"
	"database/sql"
	"fmt"
)

// GetSyncState returns the last-known sync state for a connector.
func (h *Handle) GetSyncState(ctx context.Context, connector string) (string, error) {
	var state string
	err := h.DB.QueryRowContext(ctx,
		"SELECT state FROM sync_state WHERE user_id = ? AND connector = ?", h.UserID, connector,
	).Scan(&state)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("cortex: get sync state: %w", err)
	}
	return state, nil
}

// SetSyncState upserts the sync state for a connector.
func (h *Handle) SetSyncState(ctx context.Context, connector, state string) error {
	_, err := h.DB.ExecContext(ctx,
		`INSERT INTO sync_state (user_id, connector, state, updated_at)
		 VALUES (?, ?, ?, now())
		 ON CONFLICT(user_id, connector) DO UPDATE SET state = excluded.state, updated_at = excluded.updated_at`,
		h.UserID, connector, state,
	)
	if err != nil {
		return fmt.Errorf("cortex: set sync state: %w", err)
	}
	return nil
}
