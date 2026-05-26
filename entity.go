package cortex

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// PutEntity upserts an entity by (name, type). If an entity with the same
// name and type already exists, its attributes, source, and updated_at are
// updated and the existing ID is set on the passed entity. Otherwise a new
// entity is inserted with a generated ULID.
func (c *Cortex) PutEntity(ctx context.Context, e *Entity) error {
	e.Confidence = coerceConfidence(e.Confidence)

	// Check for existing entity with same name + type.
	var existingID string
	err := c.db.QueryRowContext(ctx,
		"SELECT id FROM entities WHERE name = ? AND type = ?",
		e.Name, e.Type,
	).Scan(&existingID)

	attrsJSON, err2 := json.Marshal(e.Attributes)
	if err2 != nil {
		return fmt.Errorf("cortex: marshal attributes: %w", err2)
	}

	now := time.Now().UTC()

	if err == nil {
		// Entity exists — update.
		_, err = c.db.ExecContext(ctx,
			`UPDATE entities SET attributes = ?, source = ?, confidence = ?, updated_at = ? WHERE id = ?`,
			string(attrsJSON), e.Source, e.Confidence, now, existingID,
		)
		if err != nil {
			return fmt.Errorf("cortex: update entity: %w", err)
		}
		e.ID = existingID
		e.UpdatedAt = now
		return nil
	}
	if err != sql.ErrNoRows {
		return fmt.Errorf("cortex: check existing entity: %w", err)
	}

	// New entity — insert.
	e.ID = newID()
	e.CreatedAt = now
	e.UpdatedAt = now

	_, err = c.db.ExecContext(ctx,
		`INSERT INTO entities (id, type, name, attributes, source, confidence, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.Type, e.Name, string(attrsJSON), e.Source, e.Confidence, e.CreatedAt, e.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("cortex: insert entity: %w", err)
	}
	return nil
}

// GetEntity retrieves an entity by ID. Returns an error if not found.
func (c *Cortex) GetEntity(ctx context.Context, id string) (*Entity, error) {
	var e Entity
	var attrsJSON sql.NullString
	err := c.db.QueryRowContext(ctx,
		`SELECT id, type, name, attributes, source, confidence, created_at, updated_at
		 FROM entities WHERE id = ?`, id,
	).Scan(&e.ID, &e.Type, &e.Name, &attrsJSON, &e.Source, &e.Confidence, &e.CreatedAt, &e.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("cortex: entity %q not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("cortex: get entity: %w", err)
	}

	if attrsJSON.Valid && attrsJSON.String != "" {
		if err := json.Unmarshal([]byte(attrsJSON.String), &e.Attributes); err != nil {
			return nil, fmt.Errorf("cortex: unmarshal attributes: %w", err)
		}
	}
	return &e, nil
}

// FindEntities returns entities matching the given filter. All filter fields
// are optional — an empty filter returns all entities.
func (c *Cortex) FindEntities(ctx context.Context, f EntityFilter) ([]Entity, error) {
	query := `SELECT id, type, name, attributes, source, confidence, created_at, updated_at FROM entities`
	var conditions []string
	var args []any

	if f.Type != "" {
		conditions = append(conditions, "type = ?")
		args = append(args, f.Type)
	}
	if f.NameLike != "" {
		conditions = append(conditions, "name LIKE ?")
		args = append(args, f.NameLike)
	}
	if f.Source != "" {
		conditions = append(conditions, "source = ?")
		args = append(args, f.Source)
	}

	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}

	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("cortex: find entities: %w", err)
	}
	defer rows.Close()

	var entities []Entity
	for rows.Next() {
		var e Entity
		var attrsJSON sql.NullString
		if err := rows.Scan(&e.ID, &e.Type, &e.Name, &attrsJSON, &e.Source, &e.Confidence, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, fmt.Errorf("cortex: scan entity: %w", err)
		}
		if attrsJSON.Valid && attrsJSON.String != "" {
			if err := json.Unmarshal([]byte(attrsJSON.String), &e.Attributes); err != nil {
				return nil, fmt.Errorf("cortex: unmarshal attributes: %w", err)
			}
		}
		entities = append(entities, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cortex: iterate entities: %w", err)
	}
	return entities, nil
}

// MergeEntities merges the drop entity into the keep entity, atomically.
// All references to dropID (relationships, memory_entities, chunks,
// embeddings) are re-targeted to keepID; duplicates that would arise
// from the re-target are collapsed; the drop entity's attributes are
// unioned into the keep entity (keep wins on conflicts); a `merged_from`
// provenance record is appended to keep's attributes; finally the drop
// entity row is deleted. On any error, all changes are rolled back.
func (c *Cortex) MergeEntities(ctx context.Context, keepID, dropID string) (MergeStats, error) {
	var stats MergeStats

	if keepID == dropID {
		return stats, fmt.Errorf("cortex: cannot merge an entity into itself")
	}

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return stats, fmt.Errorf("cortex: begin tx: %w", err)
	}
	defer tx.Rollback()

	keep, err := getEntityTx(ctx, tx, keepID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return stats, fmt.Errorf("cortex: keep entity not found: %s", keepID)
		}
		return stats, fmt.Errorf("cortex: load keep entity: %w", err)
	}
	drop, err := getEntityTx(ctx, tx, dropID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return stats, fmt.Errorf("cortex: drop entity not found: %s", dropID)
		}
		return stats, fmt.Errorf("cortex: load drop entity: %w", err)
	}
	if keep.Type != drop.Type {
		return stats, fmt.Errorf("cortex: cannot merge across types: %s (keep) vs %s (drop)", keep.Type, drop.Type)
	}

	// Re-target chunks (no collision possible).
	res, err := tx.ExecContext(ctx,
		`UPDATE chunks SET entity_id = ? WHERE entity_id = ?`, keepID, dropID)
	if err != nil {
		return stats, fmt.Errorf("cortex: re-target chunks: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.Chunks = int(n)
	}

	// Re-target memory_entities, with dedup on (memory_id, entity_id) PK.
	res, err = tx.ExecContext(ctx,
		`DELETE FROM memory_entities
		 WHERE entity_id = ?
		   AND memory_id IN (SELECT memory_id FROM memory_entities WHERE entity_id = ?)`,
		dropID, keepID)
	if err != nil {
		return stats, fmt.Errorf("cortex: dedup memory_entities: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.DupesDropped += int(n)
	}
	res, err = tx.ExecContext(ctx,
		`UPDATE memory_entities SET entity_id = ? WHERE entity_id = ?`, keepID, dropID)
	if err != nil {
		return stats, fmt.Errorf("cortex: re-target memory_entities: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.Memories = int(n)
	}

	// Re-target relationships source_id with dedup on (source, target, type).
	res, err = tx.ExecContext(ctx,
		`DELETE FROM relationships
		 WHERE source_id = ?
		   AND EXISTS (
			 SELECT 1 FROM relationships k
			 WHERE k.source_id = ?
			   AND k.target_id = relationships.target_id
			   AND k.type      = relationships.type
		   )`,
		dropID, keepID)
	if err != nil {
		return stats, fmt.Errorf("cortex: dedup source rels: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.DupesDropped += int(n)
	}
	res, err = tx.ExecContext(ctx,
		`UPDATE relationships SET source_id = ? WHERE source_id = ?`, keepID, dropID)
	if err != nil {
		return stats, fmt.Errorf("cortex: re-target source rels: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.Relationships += int(n)
	}

	// Same for target_id.
	res, err = tx.ExecContext(ctx,
		`DELETE FROM relationships
		 WHERE target_id = ?
		   AND EXISTS (
			 SELECT 1 FROM relationships k
			 WHERE k.target_id = ?
			   AND k.source_id = relationships.source_id
			   AND k.type      = relationships.type
		   )`,
		dropID, keepID)
	if err != nil {
		return stats, fmt.Errorf("cortex: dedup target rels: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.DupesDropped += int(n)
	}
	res, err = tx.ExecContext(ctx,
		`UPDATE relationships SET target_id = ? WHERE target_id = ?`, keepID, dropID)
	if err != nil {
		return stats, fmt.Errorf("cortex: re-target target rels: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.Relationships += int(n)
	}

	// Drop self-loops created by the merge.
	res, err = tx.ExecContext(ctx,
		`DELETE FROM relationships WHERE source_id = ? AND target_id = ?`, keepID, keepID)
	if err != nil {
		return stats, fmt.Errorf("cortex: drop self-loops: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.DupesDropped += int(n)
	}

	// Drop the stale entity embedding.
	res, err = tx.ExecContext(ctx,
		`DELETE FROM embeddings WHERE ref_id = ? AND ref_type = 'entity'`, dropID)
	if err != nil {
		return stats, fmt.Errorf("cortex: drop embedding: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.Embeddings = int(n)
	}

	// Union attributes (keep wins) + record provenance.
	if keep.Attributes == nil {
		keep.Attributes = map[string]any{}
	}
	for k, v := range drop.Attributes {
		if _, exists := keep.Attributes[k]; exists {
			stats.AttrConflicts++
			continue
		}
		keep.Attributes[k] = v
	}
	record := mergeRecord{
		ID:       drop.ID,
		Name:     drop.Name,
		Type:     drop.Type,
		Source:   drop.Source,
		Attrs:    drop.Attributes,
		MergedAt: time.Now().UTC(),
	}
	var existing []any
	if raw, ok := keep.Attributes["merged_from"]; ok {
		if asArr, isArr := raw.([]any); isArr {
			existing = asArr
		}
	}
	existing = append(existing, record)
	keep.Attributes["merged_from"] = existing

	attrsJSON, err := json.Marshal(keep.Attributes)
	if err != nil {
		return stats, fmt.Errorf("cortex: marshal merged attributes: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE entities SET attributes = ?, updated_at = ? WHERE id = ?`,
		string(attrsJSON), time.Now().UTC(), keepID); err != nil {
		return stats, fmt.Errorf("cortex: update keep attributes: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM entities WHERE id = ?`, dropID); err != nil {
		return stats, fmt.Errorf("cortex: delete drop entity: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return stats, fmt.Errorf("cortex: commit merge: %w", err)
	}
	return stats, nil
}

// getEntityTx loads an entity by ID using the supplied transaction.
// Returns sql.ErrNoRows if not found.
func getEntityTx(ctx context.Context, tx *sql.Tx, id string) (*Entity, error) {
	var e Entity
	var attrsJSON sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT id, type, name, attributes, source, confidence, created_at, updated_at
		 FROM entities WHERE id = ?`, id,
	).Scan(&e.ID, &e.Type, &e.Name, &attrsJSON, &e.Source, &e.Confidence, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if attrsJSON.Valid && attrsJSON.String != "" {
		if err := json.Unmarshal([]byte(attrsJSON.String), &e.Attributes); err != nil {
			return nil, fmt.Errorf("cortex: unmarshal attributes: %w", err)
		}
	}
	return &e, nil
}
