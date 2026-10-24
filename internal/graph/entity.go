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

	if c.Encrypted() {
		return c.putEntityEnc(ctx, e)
	}

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
				   source = CASE WHEN is_owner THEN source ELSE ? END,
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
		 ON CONFLICT (user_id, name, type) WHERE deleted_at IS NULL AND enc IS NOT TRUE DO UPDATE SET
		   attributes = EXCLUDED.attributes,
		   source = CASE WHEN entities.is_owner THEN entities.source ELSE EXCLUDED.source END,
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
	e, err := c.scanEntity(c.DB.QueryRowContext(ctx,
		`SELECT `+entCols+` FROM entities WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, id, c.UserID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrEntityNotFound()
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

// FindEntities returns entities matching the given filter. All filter fields
// are optional — an empty filter returns all entities. Encrypted rows are
// filtered after decryption (type, name and source are ciphertext).
func (c Ops) FindEntities(ctx context.Context, f model.EntityFilter) ([]model.Entity, error) {
	if c.Encrypted() {
		return c.findEntitiesEnc(ctx, f)
	}
	query := `SELECT ` + entCols + ` FROM entities`
	conditions := []string{"user_id = ?", "deleted_at IS NULL"}
	args := []any{c.UserID}

	// Legacy rows filter in SQL; encrypted rows pass through and are
	// filtered in Go below.
	if f.Type != "" {
		conditions = append(conditions, "(enc OR "+ontology.NormTypeSQL("type")+" = ?)")
		args = append(args, ontology.NormalizeType(f.Type))
	}
	if f.NameLike != "" {
		conditions = append(conditions, "(enc OR name ILIKE ?)")
		args = append(args, f.NameLike)
	}
	if f.Source != "" {
		conditions = append(conditions, "(enc OR source = ?)")
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
		r, err := c.scanEntityRow(rows, false)
		if err != nil {
			return nil, err
		}
		if r.enc {
			if f.Type != "" && ontology.NormalizeType(r.Type) != ontology.NormalizeType(f.Type) {
				continue
			}
			if f.NameLike != "" && !ilikeMatch(f.NameLike, r.Name) {
				continue
			}
			if f.Source != "" && r.Source != f.Source {
				continue
			}
		}
		entities = append(entities, r.Entity)
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
	stats, err = c.mergeEntitiesTx(ctx, tx, keepID, dropID, mergeOpts{batchID: batchID, by: "user"})
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
	return c.mergeEntitiesTx(ctx, tx, keepID, dropID, mergeOpts{batchID: batchID, by: "user"})
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

// changeRowID is the AAD row id of one undo snapshot: batch plus row id.
func changeRowID(batchID string, id int64) string { return fmt.Sprintf("%s/%d", batchID, id) }

// recordChanges snapshots the rows of table matching where into the batch's
// change records, before the caller modifies or deletes them. extra is a JSON
// object merged into each snapshot (e.g. the retarget destination).
func (c Ops) recordChanges(ctx context.Context, tx *store.Tx, mo mergeOpts, userID, table, op, extra, where string, args ...any) error {
	if mo.batchID == "" {
		return nil
	}
	if !c.Encrypted() {
		all := append([]any{mo.batchID, userID, table, op, extra}, args...)
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO delete_batch_changes (batch_id, user_id, table_name, op, row_json)
			 SELECT ?, ?, ?, ?, to_jsonb(t) || ?::jsonb FROM `+table+` t WHERE `+where, all...); err != nil {
			return fmt.Errorf("cortex: record %s %s: %w", table, op, err)
		}
		return nil
	}
	// Encrypted user: the snapshot is sealed whole (AAD column row_json, row
	// id = batch id and change id) so the undo log holds no readable row content.
	if err := c.RequireKey(); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT (to_jsonb(t) || ?::jsonb)::text FROM `+table+` t WHERE `+where, append([]any{extra}, args...)...)
	if err != nil {
		return fmt.Errorf("cortex: record %s %s: %w", table, op, err)
	}
	var snaps []string
	for rows.Next() {
		var j string
		if err := rows.Scan(&j); err != nil {
			rows.Close()
			return fmt.Errorf("cortex: record %s %s: %w", table, op, err)
		}
		snaps = append(snaps, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("cortex: record %s %s: %w", table, op, err)
	}
	for _, j := range snaps {
		// Insert first so the row's own id can bind the ciphertext: two
		// snapshots in one batch must not be swappable.
		var id int64
		r, err := tx.QueryContext(ctx,
			`INSERT INTO delete_batch_changes (batch_id, user_id, table_name, op, row_json, row_ct, enc)
			 VALUES (?, ?, ?, ?, NULL, NULL, TRUE) RETURNING id`, mo.batchID, userID, table, op)
		if err != nil {
			return fmt.Errorf("cortex: record %s %s: %w", table, op, err)
		}
		if r.Next() {
			err = r.Scan(&id)
		}
		r.Close()
		if err != nil {
			return fmt.Errorf("cortex: record %s %s: %w", table, op, err)
		}
		ct, err := c.SealStr("delete_batch_changes", changeRowID(mo.batchID, id), "row_json", j)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE delete_batch_changes SET row_ct = ? WHERE id = ?`, ct, id); err != nil {
			return fmt.Errorf("cortex: record %s %s: %w", table, op, err)
		}
	}
	return nil
}

// mergeEntitiesTx performs the merge against the provided transaction.
// Caller is responsible for Commit or Rollback.
func (c Ops) mergeEntitiesTx(ctx context.Context, tx *store.Tx, keepID, dropID string, mo mergeOpts) (model.MergeStats, error) {
	userID := c.UserID
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

	keep, err := c.getEntityTx(ctx, tx, keepID, false)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return stats, model.NotFound("cortex: keep entity not found")
		}
		return stats, fmt.Errorf("cortex: load keep entity: %w", err)
	}
	drop, err := c.getEntityTx(ctx, tx, dropID, !soft)
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
	if err := c.recordChanges(ctx, tx, mo, userID, "entities", "update", "{}", "t.id = ? AND t.user_id = ?", keepID, userID); err != nil {
		return stats, err
	}

	// Re-target chunks (no collision possible).
	if err := c.recordChanges(ctx, tx, mo, userID, "chunks", "retarget", extraTo, "t.entity_id = ? AND t.user_id = ?", dropID, userID); err != nil {
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
	if err := c.recordChanges(ctx, tx, mo, userID, "memory_entities", "delete", "{}",
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
	if err := c.recordChanges(ctx, tx, mo, userID, "memory_entities", "retarget", extraTo, "t.entity_id = ? AND t.user_id = ?", dropID, userID); err != nil {
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
	if err := c.recordChanges(ctx, tx, mo, userID, "relationships", "delete", "{}",
		`t.source_id = ? AND t.user_id = ? AND EXISTS (SELECT 1 FROM relationships k WHERE k.source_id = ? AND k.user_id = ? AND k.target_id = t.target_id AND `+sameRelType("k", "t")+`)`,
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
			   AND `+sameRelType("k", "relationships")+`
		   )`,
		dropID, userID, keepID, userID)
	if err != nil {
		return stats, fmt.Errorf("cortex: dedup source rels: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.DupesDropped += int(n)
	}
	if err := c.recordChanges(ctx, tx, mo, userID, "relationships", "retarget", extraTo, "t.source_id = ? AND t.user_id = ?", dropID, userID); err != nil {
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
	if err := c.recordChanges(ctx, tx, mo, userID, "relationships", "delete", "{}",
		`t.target_id = ? AND t.user_id = ? AND EXISTS (SELECT 1 FROM relationships k WHERE k.target_id = ? AND k.user_id = ? AND k.source_id = t.source_id AND `+sameRelType("k", "t")+`)`,
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
			   AND `+sameRelType("k", "relationships")+`
		   )`,
		dropID, userID, keepID, userID)
	if err != nil {
		return stats, fmt.Errorf("cortex: dedup target rels: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.DupesDropped += int(n)
	}
	if err := c.recordChanges(ctx, tx, mo, userID, "relationships", "retarget", extraTo, "t.target_id = ? AND t.user_id = ?", dropID, userID); err != nil {
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
	if err := c.recordChanges(ctx, tx, mo, userID, "relationships", "delete", "{}", "t.source_id = ? AND t.target_id = ? AND t.user_id = ?", keepID, keepID, userID); err != nil {
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
	if err := c.recordChanges(ctx, tx, mo, userID, "entity_aliases", "retarget", extraTo, "t.entity_id = ? AND t.user_id = ?", dropID, userID); err != nil {
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
		inserted, err := c.insertMergeNameAlias(ctx, tx, keepID, drop.Name, norm)
		if err != nil {
			return stats, err
		}
		if inserted {
			where, wargs := "t.user_id = ? AND t.kind = 'name' AND t.value_norm = ?", []any{userID, norm}
			if c.Encrypted() {
				where, wargs = "t.user_id = ? AND t.kind = 'name' AND t.enc AND t.value_blind = ?", []any{userID, c.Blind(norm)}
			}
			if err := c.recordChanges(ctx, tx, mo, userID, "entity_aliases", "insert", "{}", where, wargs...); err != nil {
				return stats, err
			}
		}
	}

	// Drop the stale entity embedding.
	if err := c.recordChanges(ctx, tx, mo, userID, "embeddings", "delete", "{}", "t.ref_id = ? AND t.ref_type = 'entity' AND t.user_id = ?", dropID, userID); err != nil {
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

	if c.Encrypted() {
		// keep.Source is the stored value, so owner protection holds.
		if _, err := c.writeEntityEnc(ctx, tx, entWrite{id: keepID, name: keep.Name, typ: keep.Type, source: keep.Source,
			attrs: keep.Attributes, conf: keep.Confidence, updated: mergeTime}); err != nil {
			return stats, fmt.Errorf("cortex: update keep attributes: %w", err)
		}
	} else {
		attrsJSON, err := json.Marshal(keep.Attributes)
		if err != nil {
			return stats, fmt.Errorf("cortex: marshal merged attributes: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE entities SET attributes = ?::jsonb, updated_at = ? WHERE id = ? AND user_id = ?`,
			string(attrsJSON), mergeTime, keepID, userID); err != nil {
			return stats, fmt.Errorf("cortex: update keep attributes: %w", err)
		}
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
func (c Ops) getEntityTx(ctx context.Context, tx *store.Tx, id string, includeDeleted bool) (*model.Entity, error) {
	return c.scanEntity(tx.QueryRowContext(ctx,
		`SELECT `+entCols+` FROM entities WHERE id = ? AND user_id = ? AND (deleted_at IS NULL OR ?)`, id, c.UserID, includeDeleted))
}

// insertMergeNameAlias records the dropped entity's name as an alias of the
// keeper (source "merge"); true when a row was added. While migrating a
// legacy alias with the same value also counts as present.
func (c Ops) insertMergeNameAlias(ctx context.Context, tx *store.Tx, keepID, name, norm string) (bool, error) {
	if c.Encrypted() {
		if c.legacyOK() {
			var legacy bool
			if err := tx.QueryRowContext(ctx,
				`SELECT EXISTS (SELECT 1 FROM entity_aliases WHERE user_id = ? AND kind = 'name' AND enc IS NOT TRUE AND value_norm = ?)`,
				c.UserID, norm).Scan(&legacy); err != nil {
				return false, fmt.Errorf("cortex: alias merged name: %w", err)
			}
			if legacy {
				return false, nil
			}
		}
		ok, err := c.insertAliasEnc(ctx, tx, keepID, model.AliasName, name, "merge")
		if err != nil {
			return false, fmt.Errorf("cortex: alias merged name: %w", err)
		}
		return ok, nil
	}
	var inserted bool
	if err := tx.QueryRowContext(ctx,
		`WITH ins AS (
		   INSERT INTO entity_aliases (user_id, entity_id, kind, value, value_norm, source)
		   VALUES (?, ?, 'name', ?, ?, 'merge') ON CONFLICT DO NOTHING RETURNING 1)
		 SELECT EXISTS (SELECT 1 FROM ins)`,
		c.UserID, keepID, name, norm).Scan(&inserted); err != nil {
		return false, fmt.Errorf("cortex: alias merged name: %w", err)
	}
	return inserted, nil
}
