package memory

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
)

// PutMemoryEdge inserts a typed memory-to-memory edge. It is idempotent on
// (source_id, target_id, type): re-inserting the same edge is a no-op (no
// error, no duplicate). The passed struct's ID (generated if empty) and
// CreatedAt are set; on a conflict no-op the row already in the table is kept
// and its id may differ from e.ID.
func (c Ops) PutMemoryEdge(ctx context.Context, e *model.MemoryEdge) error {
	if err := store.RequireMemory(ctx, c.DB, c.UserID, e.SourceID); err != nil {
		return err
	}
	if err := store.RequireMemory(ctx, c.DB, c.UserID, e.TargetID); err != nil {
		return err
	}
	if e.ID == "" {
		e.ID = store.NewID()
	}
	e.CreatedAt = time.Now().UTC()

	var source any
	if e.Source != "" {
		source = e.Source
	}
	_, err := c.DB.ExecContext(ctx,
		`INSERT INTO memory_edges (id, user_id, source_id, target_id, type, source, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (user_id, source_id, target_id, type) DO NOTHING`,
		e.ID, c.UserID, e.SourceID, e.TargetID, e.Type, source, e.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("cortex: insert memory edge: %w", err)
	}
	return nil
}

// GetMemoryEdges returns every edge where memoryID is the source or the
// target — the memory's full edge neighborhood.
func (c Ops) GetMemoryEdges(ctx context.Context, memoryID string) ([]model.MemoryEdge, error) {
	if err := store.RequireMemory(ctx, c.DB, c.UserID, memoryID); err != nil {
		return nil, err
	}
	return c.queryMemoryEdges(ctx,
		`SELECT id, source_id, target_id, type, source, created_at
		 FROM memory_edges
		 WHERE user_id = ? AND (source_id = ? OR target_id = ?)
		 ORDER BY created_at`,
		c.UserID, memoryID, memoryID,
	)
}

// GetMemoryEdgesByType returns edges of the given type where memoryID is the
// source or the target.
func (c Ops) GetMemoryEdgesByType(ctx context.Context, memoryID, edgeType string) ([]model.MemoryEdge, error) {
	if err := store.RequireMemory(ctx, c.DB, c.UserID, memoryID); err != nil {
		return nil, err
	}
	return c.queryMemoryEdges(ctx,
		`SELECT id, source_id, target_id, type, source, created_at
		 FROM memory_edges
		 WHERE user_id = ? AND (source_id = ? OR target_id = ?) AND type = ?
		 ORDER BY created_at`,
		c.UserID, memoryID, memoryID, edgeType,
	)
}

func (c Ops) queryMemoryEdges(ctx context.Context, query string, args ...any) ([]model.MemoryEdge, error) {
	rows, err := c.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("cortex: query memory edges: %w", err)
	}
	defer rows.Close()

	var edges []model.MemoryEdge
	for rows.Next() {
		var e model.MemoryEdge
		var source sql.NullString
		if err := rows.Scan(&e.ID, &e.SourceID, &e.TargetID, &e.Type, &source, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("cortex: scan memory edge: %w", err)
		}
		if source.Valid {
			e.Source = source.String
		}
		edges = append(edges, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cortex: iterate memory edges: %w", err)
	}
	return edges, nil
}
