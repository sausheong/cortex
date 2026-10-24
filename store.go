package cortex

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/sausheong/cortex/internal/store"
)

// Cortex is the main knowledge graph store backed by Postgres.
type Cortex struct {
	h *store.Handle
}

// WithConnection returns a copy of the handle whose writes record connID as
// written_by_connection_id.
func (c *Cortex) WithConnection(connID string) *Cortex {
	h2 := *c.h
	h2.ConnID = connID
	return &Cortex{h: &h2}
}

// ConnectionID is the connection this handle stamps on its writes, "" when none.
func (c *Cortex) ConnectionID() string { return c.h.ConnID }

// Open connects to the Postgres database at dsn and applies pending migrations.
// Options are applied before schema setup.
func Open(dsn string, opts ...Option) (*Cortex, error) {
	h, err := store.Connect(dsn, opts...)
	if err != nil {
		return nil, err
	}
	c := &Cortex{h: h}
	mctx, mcancel := h.MigrationContext()
	defer mcancel()
	if err := h.Migrate(mctx); err != nil {
		h.Close()
		return nil, err
	}
	if err := c.ensureVectorSchema(mctx); err != nil {
		h.Close()
		return nil, err
	}
	return c, nil
}

// Close closes the underlying database connection.
func (c *Cortex) Close() error { return c.h.Close() }

// SetExtractor sets the extractor on the Cortex instance.
func (c *Cortex) SetExtractor(e Extractor) { c.h.Cfg.Extractor = e }

// SetLLM sets the LLM on the Cortex instance.
func (c *Cortex) SetLLM(l LLM) { c.h.Cfg.LLM = l }

// SetEmbedder sets the embedder on the Cortex instance.
func (c *Cortex) SetEmbedder(e Embedder) { c.h.Cfg.Embedder = e }

// GetSyncState returns the last-known sync state for a connector.
func (c *Cortex) GetSyncState(ctx context.Context, connector string) (string, error) {
	var state string
	err := c.h.DB.QueryRowContext(ctx,
		"SELECT state FROM sync_state WHERE user_id = ? AND connector = ?", c.h.UserID, connector,
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
func (c *Cortex) SetSyncState(ctx context.Context, connector, state string) error {
	_, err := c.h.DB.ExecContext(ctx,
		`INSERT INTO sync_state (user_id, connector, state, updated_at)
		 VALUES (?, ?, ?, now())
		 ON CONFLICT(user_id, connector) DO UPDATE SET state = excluded.state, updated_at = excluded.updated_at`,
		c.h.UserID, connector, state,
	)
	if err != nil {
		return fmt.Errorf("cortex: set sync state: %w", err)
	}
	return nil
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }
