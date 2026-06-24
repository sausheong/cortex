package cortex

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// PutMemoryEdge inserts a typed memory-to-memory edge. It is idempotent on
// (source_id, target_id, type): re-inserting the same edge is a no-op (no
// error, no duplicate). The passed struct's ID (generated if empty) and
// CreatedAt are set; on a conflict no-op the row already in the table is kept
// and its id may differ from e.ID.
func (c *Cortex) PutMemoryEdge(ctx context.Context, e *MemoryEdge) error {
	if e.ID == "" {
		e.ID = newID()
	}
	e.CreatedAt = time.Now().UTC()

	var source any
	if e.Source != "" {
		source = e.Source
	}
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO memory_edges (id, source_id, target_id, type, source, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT (source_id, target_id, type) DO NOTHING`,
		e.ID, e.SourceID, e.TargetID, e.Type, source, e.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("cortex: insert memory edge: %w", err)
	}
	return nil
}

// GetMemoryEdges returns every edge where memoryID is the source or the
// target — the memory's full edge neighborhood.
func (c *Cortex) GetMemoryEdges(ctx context.Context, memoryID string) ([]MemoryEdge, error) {
	return c.queryMemoryEdges(ctx,
		`SELECT id, source_id, target_id, type, source, created_at
		 FROM memory_edges
		 WHERE source_id = ? OR target_id = ?
		 ORDER BY created_at`,
		memoryID, memoryID,
	)
}

// GetMemoryEdgesByType returns edges of the given type where memoryID is the
// source or the target.
func (c *Cortex) GetMemoryEdgesByType(ctx context.Context, memoryID, edgeType string) ([]MemoryEdge, error) {
	return c.queryMemoryEdges(ctx,
		`SELECT id, source_id, target_id, type, source, created_at
		 FROM memory_edges
		 WHERE (source_id = ? OR target_id = ?) AND type = ?
		 ORDER BY created_at`,
		memoryID, memoryID, edgeType,
	)
}

func (c *Cortex) queryMemoryEdges(ctx context.Context, query string, args ...any) ([]MemoryEdge, error) {
	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("cortex: query memory edges: %w", err)
	}
	defer rows.Close()

	var edges []MemoryEdge
	for rows.Next() {
		var e MemoryEdge
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
