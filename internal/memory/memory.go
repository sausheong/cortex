package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
)

// PutMemory inserts a memory and its entity links into the database.
// The operation is wrapped in a transaction. The memory's ID, CreatedAt,
// and UpdatedAt are set on the passed struct.
func (c Ops) PutMemory(ctx context.Context, m *model.Memory) error {
	if err := c.RequireKey(); err != nil {
		return err
	}
	m.Confidence = model.CoerceConfidence(m.Confidence)
	m.ID = store.NewID()
	now := time.Now().UTC()
	m.CreatedAt = now
	m.UpdatedAt = now

	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cortex: begin transaction: %w", err)
	}
	defer tx.Rollback()

	for _, entityID := range m.EntityIDs {
		if err := store.RequireEntity(ctx, tx, c.UserID, entityID); err != nil {
			return err
		}
	}

	if err := c.insertMemoryEnc(ctx, tx, m); err != nil {
		return err
	}

	// Insert entity links into junction table.
	for _, entityID := range m.EntityIDs {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO memory_entities (user_id, memory_id, entity_id) VALUES (?, ?, ?)`,
			c.UserID, m.ID, entityID,
		)
		if err != nil {
			return fmt.Errorf("cortex: insert memory entity link: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cortex: commit memory: %w", err)
	}
	c.NotifyWrite("memory")
	return nil
}

// SearchMemories searches memory content over the decrypted working set,
// ranked by relevance (best first). Entity links are loaded from
// memory_entities for each result.
func (c Ops) SearchMemories(ctx context.Context, query string, limit int) ([]model.Memory, error) {
	return c.SearchMemoriesMode(ctx, query, limit, model.TemporalMode{})
}

// SearchMemoriesMode is the temporal-mode-aware backing for SearchMemories.
// The validity predicate is derived from mode (default = currently-valid).
func (c Ops) SearchMemoriesMode(ctx context.Context, query string, limit int, mode model.TemporalMode) ([]model.Memory, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	return c.searchMemoriesEnc(ctx, query, limit, mode)
}

// GetMemoriesByEntity returns all memories linked to the given entity.
// Entity links are loaded from memory_entities for each result.
func (c Ops) GetMemoriesByEntity(ctx context.Context, entityID string) ([]model.Memory, error) {
	return c.GetMemoriesByEntityMode(ctx, entityID, model.TemporalMode{})
}

// GetMemoriesByEntityMode is the temporal-mode-aware backing for
// GetMemoriesByEntity. The validity predicate is derived from mode
// (default = currently-valid).
func (c Ops) GetMemoriesByEntityMode(ctx context.Context, entityID string, mode model.TemporalMode) ([]model.Memory, error) {
	if err := store.RequireEntity(ctx, c.DB, c.UserID, entityID); err != nil {
		return nil, err
	}
	clause, targs := mode.Clause("m")
	// Arg order: user id, entity_id, then temporal args.
	args := []any{c.UserID, entityID}
	args = append(args, targs...)

	rows, err := c.DB.QueryContext(ctx,
		`SELECT `+memCols+`
		 FROM memories m
		 JOIN memory_entities me ON m.id = me.memory_id AND me.user_id = m.user_id
		 WHERE m.user_id = ? AND m.deleted_at IS NULL AND me.entity_id = ? AND `+clause,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("cortex: get memories by entity: %w", err)
	}
	defer rows.Close()

	var memories []model.Memory
	for rows.Next() {
		m, err := c.scanMemory(rows)
		if err != nil {
			return nil, err
		}
		memories = append(memories, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cortex: iterate memories: %w", err)
	}

	// Load entity links for each memory.
	for i := range memories {
		entityIDs, err := c.loadMemoryEntityIDs(ctx, memories[i].ID)
		if err != nil {
			return nil, err
		}
		memories[i].EntityIDs = entityIDs
	}
	return memories, nil
}

// loadMemoryEntityIDs loads entity IDs linked to a memory from the
// memory_entities junction table.
func (c Ops) loadMemoryEntityIDs(ctx context.Context, memoryID string) ([]string, error) {
	rows, err := c.DB.QueryContext(ctx,
		`SELECT entity_id FROM memory_entities WHERE user_id = ? AND memory_id = ?`,
		c.UserID, memoryID,
	)
	if err != nil {
		return nil, fmt.Errorf("cortex: load memory entity IDs: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("cortex: scan entity ID: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cortex: iterate entity IDs: %w", err)
	}
	return ids, nil
}

// InvalidateMemory soft-retires a memory: it sets expired_at to now (the
// system has retired this memory) and, when invalidAt is non-nil, sets
// invalid_at to the event time the fact stopped being true. The row, its
// embedding are left intact so the memory remains
// available to point-in-time history queries; default recall hides it via
// the currently-valid predicate. Returns an error if no memory has the
// given id. This is the primitive Phase 2b's reconciliation calls on
// supersession; it never deletes (use Forget for hard deletion).
func (c Ops) InvalidateMemory(ctx context.Context, id string, invalidAt *time.Time) error {
	now := time.Now().UTC()

	var res sql.Result
	var err error
	if invalidAt != nil {
		res, err = c.DB.ExecContext(ctx,
			`UPDATE memories SET expired_at = ?, invalid_at = ?, updated_at = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL`,
			now, invalidAt.UTC(), now, id, c.UserID,
		)
	} else {
		res, err = c.DB.ExecContext(ctx,
			`UPDATE memories SET expired_at = ?, updated_at = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL`,
			now, now, id, c.UserID,
		)
	}
	if err != nil {
		return fmt.Errorf("cortex: invalidate memory: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("cortex: invalidate memory rows affected: %w", err)
	}
	if n == 0 {
		return store.ErrMemoryNotFound()
	}
	return nil
}

// GetMemoryByID loads a single memory by id regardless of validity state
// (no currently-valid filter), including its temporal fields. Returns
// (nil, nil) if no such memory exists. Used by reconciliation's apply-time
// re-validation, which must distinguish "missing" from "already invalidated".
func (c Ops) GetMemoryByID(ctx context.Context, id string) (*model.Memory, error) {
	m, err := c.scanMemory(c.DB.QueryRowContext(ctx,
		`SELECT `+memCols+` FROM memories m WHERE m.id = ? AND m.user_id = ? AND m.deleted_at IS NULL`, id, c.UserID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrMemoryNotFound()
	}
	if err != nil {
		return nil, fmt.Errorf("cortex: get memory by id: %w", err)
	}
	return &m, nil
}
