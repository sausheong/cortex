package graph

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/ontology"
	"github.com/sausheong/cortex/internal/store"
)

// PutEntity upserts an entity by (name, type), with the type normalised by
// NormalizeType. If an entity with the same name and type already exists,
// its attributes, source, and updated_at are updated and the existing ID is
// set on the passed entity. Otherwise a new entity is inserted with a
// generated ULID. When e.ID names a live entity (as after GetEntity), that
// row is updated in place instead, keeping its stored type spelling if it
// normalises to the same type, so a read-modify-write never forks a row
// whose type predates normalisation.
func (c Ops) PutEntity(ctx context.Context, e *model.Entity) error {
	e.Confidence = model.CoerceConfidence(e.Confidence)
	e.Type = ontology.NormalizeType(e.Type)

	attrsJSON, err := json.Marshal(e.Attributes)
	if err != nil {
		return fmt.Errorf("cortex: marshal attributes: %w", err)
	}

	now := time.Now().UTC()

	if e.ID != "" {
		var storedType string
		err := c.DB.QueryRowContext(ctx,
			`SELECT type FROM entities WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, e.ID, c.UserID).Scan(&storedType)
		switch {
		case err == nil:
			if ontology.NormalizeType(storedType) == e.Type {
				e.Type = storedType
			}
			if err := c.DB.QueryRowContext(ctx,
				`UPDATE entities SET type = ?, name = ?, attributes = ?::jsonb,
				   source = CASE WHEN source = 'owner' THEN source ELSE ? END,
				   confidence = ?, updated_at = ?,
				   written_by_connection_id = COALESCE(?, written_by_connection_id)
				 WHERE id = ? AND user_id = ?
				 RETURNING created_at`,
				e.Type, e.Name, string(attrsJSON), e.Source, e.Confidence, now, c.NullConn(), e.ID, c.UserID,
			).Scan(&e.CreatedAt); err != nil {
				return fmt.Errorf("cortex: update entity: %w", err)
			}
			e.UpdatedAt = now
			e.WrittenBy = c.ConnID
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("cortex: load entity: %w", err)
		}
	}

	// Single atomic upsert: concurrent writers with the same (name, type)
	// converge on one row.
	err = c.DB.QueryRowContext(ctx,
		`INSERT INTO entities (id, user_id, type, name, attributes, source, confidence, created_at, updated_at, written_by_connection_id)
		 VALUES (?, ?, ?, ?, ?::jsonb, ?, ?, ?, ?, ?)
		 ON CONFLICT (user_id, name, type) WHERE deleted_at IS NULL DO UPDATE SET
		   attributes = EXCLUDED.attributes,
		   source = CASE WHEN entities.source = 'owner' THEN entities.source ELSE EXCLUDED.source END,
		   confidence = EXCLUDED.confidence,
		   updated_at = EXCLUDED.updated_at,
		   written_by_connection_id = COALESCE(EXCLUDED.written_by_connection_id, entities.written_by_connection_id)
		 RETURNING id, created_at`,
		store.NewID(), c.UserID, e.Type, e.Name, string(attrsJSON), e.Source, e.Confidence, now, now, c.NullConn(),
	).Scan(&e.ID, &e.CreatedAt)
	if err != nil {
		return fmt.Errorf("cortex: upsert entity: %w", err)
	}
	e.UpdatedAt = now
	e.WrittenBy = c.ConnID
	return nil
}

// GetEntity retrieves an entity by ID. Returns an error if not found.
func (c Ops) GetEntity(ctx context.Context, id string) (*model.Entity, error) {
	var e model.Entity
	var attrsJSON sql.NullString
	err := c.DB.QueryRowContext(ctx,
		`SELECT id, type, name, attributes, source, confidence, created_at, updated_at, COALESCE(written_by_connection_id, '')
		 FROM entities WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, id, c.UserID,
	).Scan(&e.ID, &e.Type, &e.Name, &attrsJSON, &e.Source, &e.Confidence, &e.CreatedAt, &e.UpdatedAt, &e.WrittenBy)
	if err == sql.ErrNoRows {
		return nil, store.ErrEntityNotFound()
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
func (c Ops) FindEntities(ctx context.Context, f model.EntityFilter) ([]model.Entity, error) {
	query := `SELECT id, type, name, attributes, source, confidence, created_at, updated_at, COALESCE(written_by_connection_id, '') FROM entities`
	conditions := []string{"user_id = ?", "deleted_at IS NULL"}
	args := []any{c.UserID}

	if f.Type != "" {
		conditions = append(conditions, ontology.NormTypeSQL("type")+" = ?")
		args = append(args, ontology.NormalizeType(f.Type))
	}
	if f.NameLike != "" {
		conditions = append(conditions, "name ILIKE ?")
		args = append(args, f.NameLike)
	}
	if f.Source != "" {
		conditions = append(conditions, "source = ?")
		args = append(args, f.Source)
	}

	query += " WHERE " + strings.Join(conditions, " AND ")

	rows, err := c.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("cortex: find entities: %w", err)
	}
	defer rows.Close()

	var entities []model.Entity
	for rows.Next() {
		var e model.Entity
		var attrsJSON sql.NullString
		if err := rows.Scan(&e.ID, &e.Type, &e.Name, &attrsJSON, &e.Source, &e.Confidence, &e.CreatedAt, &e.UpdatedAt, &e.WrittenBy); err != nil {
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
func (c Ops) MergeEntities(ctx context.Context, keepID, dropID string) (model.MergeStats, error) {
	var stats model.MergeStats
	if keepID == dropID {
		return stats, fmt.Errorf("cortex: cannot merge an entity into itself")
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return stats, fmt.Errorf("cortex: begin tx: %w", err)
	}
	defer tx.Rollback()
	batchID, err := NewBatch(ctx, tx, c.UserID, "merge")
	if err != nil {
		return stats, err
	}
	stats, err = mergeEntitiesTx(ctx, tx, c.UserID, keepID, dropID, mergeOpts{batchID: batchID, by: "user"})
	if err != nil {
		return stats, err
	}
	stats.BatchID = batchID
	if err := tx.Commit(); err != nil {
		return stats, fmt.Errorf("cortex: commit merge: %w", err)
	}
	return stats, nil
}

// MergeEntitiesDryRun runs the merge algorithm but always ROLLBACKs the
// transaction, returning the same MergeStats a real run would produce
// without writing any changes. Used by `cortex merge --dry-run`.
func (c Ops) MergeEntitiesDryRun(ctx context.Context, keepID, dropID string) (model.MergeStats, error) {
	var stats model.MergeStats
	if keepID == dropID {
		return stats, fmt.Errorf("cortex: cannot merge an entity into itself")
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return stats, fmt.Errorf("cortex: begin tx: %w", err)
	}
	defer tx.Rollback()
	batchID, err := NewBatch(ctx, tx, c.UserID, "merge")
	if err != nil {
		return stats, err
	}
	return mergeEntitiesTx(ctx, tx, c.UserID, keepID, dropID, mergeOpts{batchID: batchID, by: "user"})
}

// mergeOpts controls how mergeEntitiesTx records and finishes a merge.
// With a batchID the merge is soft and reversible: every change is recorded
// and the dropped entity is soft-deleted into that batch. With an empty
// batchID (restore collisions) nothing is recorded, the dropped entity may
// already be soft-deleted, and it is removed for good.
type mergeOpts struct {
	batchID string
	by      string
	// sameKind allows merging entities whose types differ only in spelling
	// or map to the same curated type (see kindMatches).
	sameKind bool
}

// recordChanges snapshots the rows of table matching where into the batch's
// change records, before the caller modifies or deletes them. extra is a JSON
// object merged into each snapshot (e.g. the retarget destination).
func recordChanges(ctx context.Context, tx *store.Tx, mo mergeOpts, userID, table, op, extra, where string, args ...any) error {
	if mo.batchID == "" {
		return nil
	}
	all := append([]any{mo.batchID, userID, table, op, extra}, args...)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO delete_batch_changes (batch_id, user_id, table_name, op, row_json)
		 SELECT ?, ?, ?, ?, to_jsonb(t) || ?::jsonb FROM `+table+` t WHERE `+where, all...); err != nil {
		return fmt.Errorf("cortex: record %s %s: %w", table, op, err)
	}
	return nil
}

// mergeEntitiesTx performs the merge against the provided transaction.
// Caller is responsible for Commit or Rollback.
func mergeEntitiesTx(ctx context.Context, tx *store.Tx, userID, keepID, dropID string, mo mergeOpts) (model.MergeStats, error) {
	var stats model.MergeStats
	soft := mo.batchID != ""
	extraTo := `{"_to":` + strconv.Quote(keepID) + `}`

	// Lock both rows (ordered by id to avoid deadlock) so concurrent upserts
	// and inserts that reference either entity serialize with the merge.
	lrows, err := tx.QueryContext(ctx,
		`SELECT id FROM entities WHERE id IN (?, ?) AND user_id = ? ORDER BY id FOR UPDATE`, keepID, dropID, userID)
	if err != nil {
		return stats, fmt.Errorf("cortex: lock entities: %w", err)
	}
	for lrows.Next() {
	}
	if err := lrows.Err(); err != nil {
		lrows.Close()
		return stats, fmt.Errorf("cortex: lock entities: %w", err)
	}
	lrows.Close()

	keep, err := getEntityTx(ctx, tx, userID, keepID, false)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return stats, model.NotFound("cortex: keep entity not found")
		}
		return stats, fmt.Errorf("cortex: load keep entity: %w", err)
	}
	drop, err := getEntityTx(ctx, tx, userID, dropID, !soft)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return stats, model.NotFound("cortex: drop entity not found")
		}
		return stats, fmt.Errorf("cortex: load drop entity: %w", err)
	}
	if keep.Type != drop.Type && !(mo.sameKind && kindMatches(drop.Type, ontology.NormalizeType(keep.Type))) {
		return stats, fmt.Errorf("cortex: cannot merge across types: %s (keep) vs %s (drop)", keep.Type, drop.Type)
	}

	// Snapshot the keeper so an undo can put its row back exactly.
	if err := recordChanges(ctx, tx, mo, userID, "entities", "update", "{}", "t.id = ? AND t.user_id = ?", keepID, userID); err != nil {
		return stats, err
	}

	// Re-target chunks (no collision possible).
	if err := recordChanges(ctx, tx, mo, userID, "chunks", "retarget", extraTo, "t.entity_id = ? AND t.user_id = ?", dropID, userID); err != nil {
		return stats, err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE chunks SET entity_id = ? WHERE entity_id = ? AND user_id = ?`, keepID, dropID, userID)
	if err != nil {
		return stats, fmt.Errorf("cortex: re-target chunks: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.Chunks = int(n)
	}

	// Re-target memory_entities, with dedup on (memory_id, entity_id) PK.
	if err := recordChanges(ctx, tx, mo, userID, "memory_entities", "delete", "{}",
		`t.entity_id = ? AND t.user_id = ? AND t.memory_id IN (SELECT memory_id FROM memory_entities WHERE entity_id = ? AND user_id = ?)`,
		dropID, userID, keepID, userID); err != nil {
		return stats, err
	}
	res, err = tx.ExecContext(ctx,
		`DELETE FROM memory_entities
		 WHERE entity_id = ? AND user_id = ?
		   AND memory_id IN (SELECT memory_id FROM memory_entities WHERE entity_id = ? AND user_id = ?)`,
		dropID, userID, keepID, userID)
	if err != nil {
		return stats, fmt.Errorf("cortex: dedup memory_entities: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.DupesDropped += int(n)
	}
	if err := recordChanges(ctx, tx, mo, userID, "memory_entities", "retarget", extraTo, "t.entity_id = ? AND t.user_id = ?", dropID, userID); err != nil {
		return stats, err
	}
	res, err = tx.ExecContext(ctx,
		`UPDATE memory_entities SET entity_id = ? WHERE entity_id = ? AND user_id = ?`, keepID, dropID, userID)
	if err != nil {
		return stats, fmt.Errorf("cortex: re-target memory_entities: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.Memories = int(n)
	}

	// Re-target relationships source_id with dedup on (source, target, type).
	if err := recordChanges(ctx, tx, mo, userID, "relationships", "delete", "{}",
		`t.source_id = ? AND t.user_id = ? AND EXISTS (SELECT 1 FROM relationships k WHERE k.source_id = ? AND k.user_id = ? AND k.target_id = t.target_id AND k.type = t.type)`,
		dropID, userID, keepID, userID); err != nil {
		return stats, err
	}
	res, err = tx.ExecContext(ctx,
		`DELETE FROM relationships
		 WHERE source_id = ? AND user_id = ?
		   AND EXISTS (
			 SELECT 1 FROM relationships k
			 WHERE k.source_id = ? AND k.user_id = ?
			   AND k.target_id = relationships.target_id
			   AND k.type      = relationships.type
		   )`,
		dropID, userID, keepID, userID)
	if err != nil {
		return stats, fmt.Errorf("cortex: dedup source rels: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.DupesDropped += int(n)
	}
	if err := recordChanges(ctx, tx, mo, userID, "relationships", "retarget", extraTo, "t.source_id = ? AND t.user_id = ?", dropID, userID); err != nil {
		return stats, err
	}
	res, err = tx.ExecContext(ctx,
		`UPDATE relationships SET source_id = ? WHERE source_id = ? AND user_id = ?`, keepID, dropID, userID)
	if err != nil {
		return stats, fmt.Errorf("cortex: re-target source rels: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.Relationships += int(n)
	}

	// Same for target_id.
	if err := recordChanges(ctx, tx, mo, userID, "relationships", "delete", "{}",
		`t.target_id = ? AND t.user_id = ? AND EXISTS (SELECT 1 FROM relationships k WHERE k.target_id = ? AND k.user_id = ? AND k.source_id = t.source_id AND k.type = t.type)`,
		dropID, userID, keepID, userID); err != nil {
		return stats, err
	}
	res, err = tx.ExecContext(ctx,
		`DELETE FROM relationships
		 WHERE target_id = ? AND user_id = ?
		   AND EXISTS (
			 SELECT 1 FROM relationships k
			 WHERE k.target_id = ? AND k.user_id = ?
			   AND k.source_id = relationships.source_id
			   AND k.type      = relationships.type
		   )`,
		dropID, userID, keepID, userID)
	if err != nil {
		return stats, fmt.Errorf("cortex: dedup target rels: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.DupesDropped += int(n)
	}
	if err := recordChanges(ctx, tx, mo, userID, "relationships", "retarget", extraTo, "t.target_id = ? AND t.user_id = ?", dropID, userID); err != nil {
		return stats, err
	}
	res, err = tx.ExecContext(ctx,
		`UPDATE relationships SET target_id = ? WHERE target_id = ? AND user_id = ?`, keepID, dropID, userID)
	if err != nil {
		return stats, fmt.Errorf("cortex: re-target target rels: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.Relationships += int(n)
	}

	// Drop self-loops created by the merge.
	if err := recordChanges(ctx, tx, mo, userID, "relationships", "delete", "{}", "t.source_id = ? AND t.target_id = ? AND t.user_id = ?", keepID, keepID, userID); err != nil {
		return stats, err
	}
	res, err = tx.ExecContext(ctx,
		`DELETE FROM relationships WHERE source_id = ? AND target_id = ? AND user_id = ?`, keepID, keepID, userID)
	if err != nil {
		return stats, fmt.Errorf("cortex: drop self-loops: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.DupesDropped += int(n)
	}

	// Re-target aliases (an alias value names one entity, so none collide),
	// then keep the dropped entity's name as an alias of the keeper so later
	// mentions of it resolve here.
	if err := recordChanges(ctx, tx, mo, userID, "entity_aliases", "retarget", extraTo, "t.entity_id = ? AND t.user_id = ?", dropID, userID); err != nil {
		return stats, err
	}
	res, err = tx.ExecContext(ctx,
		`UPDATE entity_aliases SET entity_id = ? WHERE entity_id = ? AND user_id = ?`, keepID, dropID, userID)
	if err != nil {
		return stats, fmt.Errorf("cortex: re-target aliases: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.Aliases = int(n)
	}
	if norm := normalizeAlias(model.AliasName, drop.Name); norm != normalizeAlias(model.AliasName, keep.Name) {
		var inserted bool
		if err := tx.QueryRowContext(ctx,
			`WITH ins AS (
			   INSERT INTO entity_aliases (user_id, entity_id, kind, value, value_norm, source)
			   VALUES (?, ?, 'name', ?, ?, 'merge') ON CONFLICT DO NOTHING RETURNING 1)
			 SELECT EXISTS (SELECT 1 FROM ins)`,
			userID, keepID, drop.Name, norm).Scan(&inserted); err != nil {
			return stats, fmt.Errorf("cortex: alias merged name: %w", err)
		}
		if inserted {
			if err := recordChanges(ctx, tx, mo, userID, "entity_aliases", "insert", "{}",
				"t.user_id = ? AND t.kind = 'name' AND t.value_norm = ?", userID, norm); err != nil {
				return stats, err
			}
		}
	}

	// Drop the stale entity embedding.
	if err := recordChanges(ctx, tx, mo, userID, "embeddings", "delete", "{}", "t.ref_id = ? AND t.ref_type = 'entity' AND t.user_id = ?", dropID, userID); err != nil {
		return stats, err
	}
	res, err = tx.ExecContext(ctx,
		`DELETE FROM embeddings WHERE ref_id = ? AND ref_type = 'entity' AND user_id = ?`, dropID, userID)
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
	mergeTime := time.Now().UTC()
	record := model.MergeRecord{
		ID:       drop.ID,
		Name:     drop.Name,
		Type:     drop.Type,
		Source:   drop.Source,
		Attrs:    drop.Attributes,
		MergedAt: mergeTime,
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
		`UPDATE entities SET attributes = ?::jsonb, updated_at = ? WHERE id = ? AND user_id = ?`,
		string(attrsJSON), mergeTime, keepID, userID); err != nil {
		return stats, fmt.Errorf("cortex: update keep attributes: %w", err)
	}

	if soft {
		if _, err := tx.ExecContext(ctx,
			`UPDATE entities SET deleted_at = ?, deleted_by = ?, delete_batch_id = ? WHERE id = ? AND user_id = ?`,
			mergeTime, mo.by, mo.batchID, dropID, userID); err != nil {
			return stats, fmt.Errorf("cortex: delete drop entity: %w", err)
		}
	} else if _, err := tx.ExecContext(ctx,
		`DELETE FROM entities WHERE id = ? AND user_id = ?`, dropID, userID); err != nil {
		return stats, fmt.Errorf("cortex: delete drop entity: %w", err)
	}

	return stats, nil
}

// getEntityTx loads an entity by ID using the supplied transaction.
// Returns sql.ErrNoRows if not found.
func getEntityTx(ctx context.Context, tx *store.Tx, userID, id string, includeDeleted bool) (*model.Entity, error) {
	var e model.Entity
	var attrsJSON sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT id, type, name, attributes, source, confidence, created_at, updated_at, COALESCE(written_by_connection_id, '')
		 FROM entities WHERE id = ? AND user_id = ? AND (deleted_at IS NULL OR ?)`, id, userID, includeDeleted,
	).Scan(&e.ID, &e.Type, &e.Name, &attrsJSON, &e.Source, &e.Confidence, &e.CreatedAt, &e.UpdatedAt, &e.WrittenBy)
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
