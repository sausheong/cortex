package cortex

import "context"

// GetSyncState returns the last-known sync state for a connector.
func (c *Cortex) GetSyncState(ctx context.Context, connector string) (string, error) {
	return c.h.GetSyncState(ctx, connector)
}

// SetSyncState upserts the sync state for a connector.
func (c *Cortex) SetSyncState(ctx context.Context, connector, state string) error {
	return c.h.SetSyncState(ctx, connector, state)
}
