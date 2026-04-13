package cortex

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// PutMemory inserts a memory and its entity links into the database.
// The operation is wrapped in a transaction. The memory's ID, CreatedAt,
// and UpdatedAt are set on the passed struct.
func (c *Cortex) PutMemory(ctx context.Context, m *Memory) error {
	m.ID = newID()
	now := time.Now().UTC()
	m.CreatedAt = now
	m.UpdatedAt = now

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cortex: begin transaction: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO memories (id, content, source, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		m.ID, m.Content, m.Source, m.CreatedAt, m.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("cortex: insert memory: %w", err)
	}

	// Insert entity links into junction table.
	for _, entityID := range m.EntityIDs {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO memory_entities (memory_id, entity_id) VALUES (?, ?)`,
			m.ID, entityID,
		)
		if err != nil {
			return fmt.Errorf("cortex: insert memory entity link: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cortex: commit memory: %w", err)
	}
	return nil
}

// SearchMemories performs a LIKE-based keyword search on memory content.
// The query is split into words and any word match counts. Entity links
// are loaded from memory_entities for each result.
func (c *Cortex) SearchMemories(ctx context.Context, query string, limit int) ([]Memory, error) {
	words := strings.Fields(query)
	if len(words) == 0 {
		return nil, nil
	}

	// Build OR conditions for each word.
	var conditions []string
	var args []any
	for _, word := range words {
		conditions = append(conditions, "content LIKE ?")
		args = append(args, "%"+word+"%")
	}

	sqlQuery := `SELECT id, content, source, created_at, updated_at
		FROM memories WHERE ` + strings.Join(conditions, " OR ") + `
		LIMIT ?`
	args = append(args, limit)

	rows, err := c.db.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("cortex: search memories: %w", err)
	}
	defer rows.Close()

	var memories []Memory
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.Content, &m.Source, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("cortex: scan memory: %w", err)
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

// GetMemoriesByEntity returns all memories linked to the given entity.
// Entity links are loaded from memory_entities for each result.
func (c *Cortex) GetMemoriesByEntity(ctx context.Context, entityID string) ([]Memory, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT m.id, m.content, m.source, m.created_at, m.updated_at
		 FROM memories m
		 JOIN memory_entities me ON m.id = me.memory_id
		 WHERE me.entity_id = ?`,
		entityID,
	)
	if err != nil {
		return nil, fmt.Errorf("cortex: get memories by entity: %w", err)
	}
	defer rows.Close()

	var memories []Memory
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.Content, &m.Source, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("cortex: scan memory: %w", err)
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
func (c *Cortex) loadMemoryEntityIDs(ctx context.Context, memoryID string) ([]string, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT entity_id FROM memory_entities WHERE memory_id = ?`,
		memoryID,
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
