package cortex

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// nullTimePtr converts a sql.NullTime to a *time.Time (nil when not valid).
func nullTimePtr(nt sql.NullTime) *time.Time {
	if !nt.Valid {
		return nil
	}
	t := nt.Time
	return &t
}

// PutMemory inserts a memory and its entity links into the database.
// The operation is wrapped in a transaction. The memory's ID, CreatedAt,
// and UpdatedAt are set on the passed struct.
func (c *Cortex) PutMemory(ctx context.Context, m *Memory) error {
	m.Confidence = coerceConfidence(m.Confidence)
	m.ID = newID()
	now := time.Now().UTC()
	m.CreatedAt = now
	m.UpdatedAt = now

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cortex: begin transaction: %w", err)
	}
	defer tx.Rollback()

	var speaker any
	if m.Speaker != "" {
		speaker = m.Speaker
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO memories (id, content, source, speaker, confidence, created_at, updated_at, valid_at, invalid_at, static)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.Content, m.Source, speaker, m.Confidence, m.CreatedAt, m.UpdatedAt, m.ValidAt, m.InvalidAt, m.Static,
	)
	if err != nil {
		return fmt.Errorf("cortex: insert memory: %w", err)
	}

	rowID, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("cortex: memory last insert id: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO memories_fts (rowid, content) VALUES (?, ?)`,
		rowID, m.Content,
	); err != nil {
		return fmt.Errorf("cortex: insert memory fts: %w", err)
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

// SearchMemories performs an FTS5 full-text search on memory content,
// ranked by relevance (best first). The query is sanitized into a safe
// FTS5 MATCH expression. Entity links are loaded from memory_entities for
// each result.
func (c *Cortex) SearchMemories(ctx context.Context, query string, limit int) ([]Memory, error) {
	return c.searchMemoriesMode(ctx, query, limit, temporalMode{})
}

// searchMemoriesMode is the temporal-mode-aware backing for SearchMemories.
// The validity predicate is derived from mode (default = currently-valid).
func (c *Cortex) searchMemoriesMode(ctx context.Context, query string, limit int, mode temporalMode) ([]Memory, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}

	clause, targs := mode.clause("m")
	// Arg order: MATCH placeholder first, then temporal args, then limit.
	args := []any{ftsQuery(query)}
	args = append(args, targs...)
	args = append(args, limit)

	rows, err := c.db.QueryContext(ctx,
		`SELECT m.id, m.content, m.source, m.speaker, m.confidence, m.created_at, m.updated_at, m.valid_at, m.invalid_at, m.expired_at, m.static
		 FROM memories m
		 JOIN memories_fts f ON m.rowid = f.rowid
		 WHERE memories_fts MATCH ? AND `+clause+`
		 ORDER BY f.rank
		 LIMIT ?`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("cortex: search memories: %w", err)
	}
	defer rows.Close()

	var memories []Memory
	for rows.Next() {
		var m Memory
		var spk sql.NullString
		var vat, iat, eat sql.NullTime
		if err := rows.Scan(&m.ID, &m.Content, &m.Source, &spk, &m.Confidence, &m.CreatedAt, &m.UpdatedAt, &vat, &iat, &eat, &m.Static); err != nil {
			return nil, fmt.Errorf("cortex: scan memory: %w", err)
		}
		if spk.Valid {
			m.Speaker = spk.String
		}
		m.ValidAt = nullTimePtr(vat)
		m.InvalidAt = nullTimePtr(iat)
		m.ExpiredAt = nullTimePtr(eat)
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
	return c.getMemoriesByEntityMode(ctx, entityID, temporalMode{})
}

// getMemoriesByEntityMode is the temporal-mode-aware backing for
// GetMemoriesByEntity. The validity predicate is derived from mode
// (default = currently-valid).
func (c *Cortex) getMemoriesByEntityMode(ctx context.Context, entityID string, mode temporalMode) ([]Memory, error) {
	clause, targs := mode.clause("m")
	// Arg order: entity_id first, then temporal args.
	args := []any{entityID}
	args = append(args, targs...)

	rows, err := c.db.QueryContext(ctx,
		`SELECT m.id, m.content, m.source, m.speaker, m.confidence, m.created_at, m.updated_at, m.valid_at, m.invalid_at, m.expired_at, m.static
		 FROM memories m
		 JOIN memory_entities me ON m.id = me.memory_id
		 WHERE me.entity_id = ? AND `+clause,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("cortex: get memories by entity: %w", err)
	}
	defer rows.Close()

	var memories []Memory
	for rows.Next() {
		var m Memory
		var spk sql.NullString
		var vat, iat, eat sql.NullTime
		if err := rows.Scan(&m.ID, &m.Content, &m.Source, &spk, &m.Confidence, &m.CreatedAt, &m.UpdatedAt, &vat, &iat, &eat, &m.Static); err != nil {
			return nil, fmt.Errorf("cortex: scan memory: %w", err)
		}
		if spk.Valid {
			m.Speaker = spk.String
		}
		m.ValidAt = nullTimePtr(vat)
		m.InvalidAt = nullTimePtr(iat)
		m.ExpiredAt = nullTimePtr(eat)
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

// ftsQuery turns an arbitrary user string into a safe FTS5 MATCH expression:
// each whitespace-separated token is wrapped in double quotes (escaping any
// embedded quotes) and OR-joined, so no token is interpreted as FTS syntax.
// Returns "" for an all-whitespace input (caller should treat as no-op).
func ftsQuery(q string) string {
	fields := strings.Fields(q)
	if len(fields) == 0 {
		return ""
	}
	quoted := make([]string, len(fields))
	for i, f := range fields {
		quoted[i] = `"` + strings.ReplaceAll(f, `"`, `""`) + `"`
	}
	return strings.Join(quoted, " OR ")
}

// InvalidateMemory soft-retires a memory: it sets expired_at to now (the
// system has retired this memory) and, when invalidAt is non-nil, sets
// invalid_at to the event time the fact stopped being true. The row, its
// FTS entry, and its embedding are left intact so the memory remains
// available to point-in-time history queries; default recall hides it via
// the currently-valid predicate. Returns an error if no memory has the
// given id. This is the primitive Phase 2b's reconciliation calls on
// supersession; it never deletes (use Forget for hard deletion).
func (c *Cortex) InvalidateMemory(ctx context.Context, id string, invalidAt *time.Time) error {
	now := time.Now().UTC()

	var res sql.Result
	var err error
	if invalidAt != nil {
		res, err = c.db.ExecContext(ctx,
			`UPDATE memories SET expired_at = ?, invalid_at = ?, updated_at = ? WHERE id = ?`,
			now, invalidAt.UTC(), now, id,
		)
	} else {
		res, err = c.db.ExecContext(ctx,
			`UPDATE memories SET expired_at = ?, updated_at = ? WHERE id = ?`,
			now, now, id,
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
		return fmt.Errorf("cortex: memory %q not found", id)
	}
	return nil
}

// getMemoryByID loads a single memory by id regardless of validity state
// (no currently-valid filter), including its temporal fields. Returns
// (nil, nil) if no such memory exists. Used by reconciliation's apply-time
// re-validation, which must distinguish "missing" from "already invalidated".
func (c *Cortex) getMemoryByID(ctx context.Context, id string) (*Memory, error) {
	var m Memory
	var spk sql.NullString
	var vat, iat, eat sql.NullTime
	err := c.db.QueryRowContext(ctx,
		`SELECT id, content, source, speaker, confidence, created_at, updated_at, valid_at, invalid_at, expired_at, static
		 FROM memories WHERE id = ?`, id,
	).Scan(&m.ID, &m.Content, &m.Source, &spk, &m.Confidence, &m.CreatedAt, &m.UpdatedAt, &vat, &iat, &eat, &m.Static)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cortex: get memory by id: %w", err)
	}
	if spk.Valid {
		m.Speaker = spk.String
	}
	m.ValidAt = nullTimePtr(vat)
	m.InvalidAt = nullTimePtr(iat)
	m.ExpiredAt = nullTimePtr(eat)
	return &m, nil
}
