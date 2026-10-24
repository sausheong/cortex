package cortex

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

func errBatchNotFound() error { return model.NotFound("cortex: delete batch not found") }

// newBatch inserts a delete_batches row for userID and returns its ID.
func newBatch(ctx context.Context, tx *store.Tx, userID, kind string) (string, error) {
	id := store.NewID()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO delete_batches (id, user_id, kind) VALUES (?, ?, ?)`, id, userID, kind); err != nil {
		return "", fmt.Errorf("cortex: create delete batch: %w", err)
	}
	return id, nil
}

func rowsAffected(res sql.Result) int {
	n, _ := res.RowsAffected()
	return int(n)
}

// deleteWhere builds the WHERE clause shared by SoftDeleteMemories and
// CountMemories.
func (c *Cortex) deleteWhere(f DeleteFilter) (string, []any, error) {
	conds := []string{"user_id = ?", "deleted_at IS NULL"}
	args := []any{c.h.UserID}
	set := f.All
	if len(f.IDs) > 0 {
		set = true
		ph := make([]string, len(f.IDs))
		for i, id := range f.IDs {
			ph[i] = "?"
			args = append(args, id)
		}
		conds = append(conds, "id IN ("+strings.Join(ph, ",")+")")
	}
	if f.WrittenBy != "" {
		set = true
		conds = append(conds, "written_by_connection_id = ?")
		args = append(args, f.WrittenBy)
	}
	if f.OlderThan != nil {
		set = true
		conds = append(conds, "created_at < ?")
		args = append(args, *f.OlderThan)
	}
	if f.Query != "" {
		set = true
		esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(f.Query)
		conds = append(conds, `content ILIKE ? ESCAPE '\'`)
		args = append(args, "%"+esc+"%")
	}
	if !set {
		return "", nil, fmt.Errorf("cortex: delete filter is empty")
	}
	return strings.Join(conds, " AND "), args, nil
}

// CountMemories returns how many live memories SoftDeleteMemories would mark
// for f, so a caller can show the exact number before deleting.
func (c *Cortex) CountMemories(ctx context.Context, f DeleteFilter) (int, error) {
	where, args, err := c.deleteWhere(f)
	if err != nil {
		return 0, err
	}
	var n int
	if err := c.h.DB.QueryRowContext(ctx, `SELECT count(*) FROM memories WHERE `+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("cortex: count memories: %w", err)
	}
	return n, nil
}

// SampleMemories returns up to limit of the live memories SoftDeleteMemories
// would mark for f, newest first, so a caller can show what is about to go.
func (c *Cortex) SampleMemories(ctx context.Context, f DeleteFilter, limit int) ([]Memory, error) {
	where, args, err := c.deleteWhere(f)
	if err != nil {
		return nil, err
	}
	rows, err := c.h.DB.QueryContext(ctx,
		`SELECT id, content, created_at FROM memories WHERE `+where+` ORDER BY created_at DESC, id LIMIT ?`,
		append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("cortex: sample memories: %w", err)
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.Content, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("cortex: scan memory: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CountByConnection returns how many rows SoftDeleteByConnection would mark
// for connID (memories, entities, chunks and relationships, including
// relationships that touch the connection's entities).
func (c *Cortex) CountByConnection(ctx context.Context, connID string) (int, error) {
	if connID == "" {
		return 0, fmt.Errorf("cortex: connection id required")
	}
	total := 0
	for _, table := range []string{"memories", "entities", "chunks"} {
		var n int
		if err := c.h.DB.QueryRowContext(ctx,
			`SELECT count(*) FROM `+table+` WHERE user_id = ? AND deleted_at IS NULL AND written_by_connection_id = ?`,
			c.h.UserID, connID).Scan(&n); err != nil {
			return 0, fmt.Errorf("cortex: count %s: %w", table, err)
		}
		total += n
	}
	var n int
	if err := c.h.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM relationships WHERE user_id = ? AND deleted_at IS NULL
		   AND (written_by_connection_id = ?
		     OR source_id IN (SELECT id FROM entities WHERE user_id = ? AND deleted_at IS NULL AND written_by_connection_id = ?)
		     OR target_id IN (SELECT id FROM entities WHERE user_id = ? AND deleted_at IS NULL AND written_by_connection_id = ?))`,
		c.h.UserID, connID, c.h.UserID, connID, c.h.UserID, connID).Scan(&n); err != nil {
		return 0, fmt.Errorf("cortex: count relationships: %w", err)
	}
	return total + n, nil
}

// SoftDeleteMemories marks the memories matching f as deleted in one batch.
// The batch ID is empty when nothing matched.
func (c *Cortex) SoftDeleteMemories(ctx context.Context, f DeleteFilter, by string) (string, int, error) {
	where, args, err := c.deleteWhere(f)
	if err != nil {
		return "", 0, err
	}

	tx, err := c.h.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, fmt.Errorf("cortex: begin transaction: %w", err)
	}
	defer tx.Rollback()
	batchID, err := newBatch(ctx, tx, c.h.UserID, "memories")
	if err != nil {
		return "", 0, err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE memories SET deleted_at = now(), deleted_by = ?, delete_batch_id = ? WHERE `+where,
		append([]any{by, batchID}, args...)...)
	if err != nil {
		return "", 0, fmt.Errorf("cortex: soft delete memories: %w", err)
	}
	n := rowsAffected(res)
	if n == 0 {
		return "", 0, nil
	}
	if err := tx.Commit(); err != nil {
		return "", 0, fmt.Errorf("cortex: commit soft delete: %w", err)
	}
	return batchID, n, nil
}

// softDeleteEntityTx soft-deletes one live entity with its relationships and
// chunks into batchID. Memory links are kept.
func (c *Cortex) softDeleteEntityTx(ctx context.Context, tx *store.Tx, batchID, by, id string) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE relationships SET deleted_at = now(), deleted_by = ?, delete_batch_id = ?
		 WHERE user_id = ? AND deleted_at IS NULL AND (source_id = ? OR target_id = ?)`,
		by, batchID, c.h.UserID, id, id); err != nil {
		return fmt.Errorf("cortex: soft delete relationships: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE chunks SET deleted_at = now(), deleted_by = ?, delete_batch_id = ?
		 WHERE user_id = ? AND deleted_at IS NULL AND entity_id = ?`,
		by, batchID, c.h.UserID, id); err != nil {
		return fmt.Errorf("cortex: soft delete chunks: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE entities SET deleted_at = now(), deleted_by = ?, delete_batch_id = ?
		 WHERE id = ? AND user_id = ? AND deleted_at IS NULL`,
		by, batchID, id, c.h.UserID); err != nil {
		return fmt.Errorf("cortex: soft delete entity: %w", err)
	}
	return nil
}

// SoftDeleteEntity soft-deletes an entity, its relationships and its chunks.
func (c *Cortex) SoftDeleteEntity(ctx context.Context, id, by string) (string, error) {
	return c.softDeleteEntity(ctx, id, by, "entity")
}

func (c *Cortex) softDeleteEntity(ctx context.Context, id, by, kind string) (string, error) {
	tx, err := c.h.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("cortex: begin transaction: %w", err)
	}
	defer tx.Rollback()
	if err := store.RequireEntity(ctx, tx, c.h.UserID, id); err != nil {
		return "", err
	}
	batchID, err := newBatch(ctx, tx, c.h.UserID, kind)
	if err != nil {
		return "", err
	}
	if err := c.softDeleteEntityTx(ctx, tx, batchID, by, id); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("cortex: commit soft delete: %w", err)
	}
	return batchID, nil
}

// SoftDeleteByConnection soft-deletes everything the connection wrote:
// memories, entities, relationships and chunks, plus relationships that touch
// the removed entities. n counts all rows marked.
func (c *Cortex) SoftDeleteByConnection(ctx context.Context, connID string) (string, int, error) {
	if connID == "" {
		return "", 0, fmt.Errorf("cortex: connection id required")
	}
	by := "connection:" + connID
	tx, err := c.h.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, fmt.Errorf("cortex: begin transaction: %w", err)
	}
	defer tx.Rollback()
	batchID, err := newBatch(ctx, tx, c.h.UserID, "connection")
	if err != nil {
		return "", 0, err
	}
	total := 0
	for _, table := range []string{"memories", "entities", "chunks", "relationships"} {
		res, err := tx.ExecContext(ctx,
			`UPDATE `+table+` SET deleted_at = now(), deleted_by = ?, delete_batch_id = ?
			 WHERE user_id = ? AND deleted_at IS NULL AND written_by_connection_id = ?`,
			by, batchID, c.h.UserID, connID)
		if err != nil {
			return "", 0, fmt.Errorf("cortex: soft delete %s: %w", table, err)
		}
		total += rowsAffected(res)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE relationships SET deleted_at = now(), deleted_by = ?, delete_batch_id = ?
		 WHERE user_id = ? AND deleted_at IS NULL
		   AND (source_id IN (SELECT id FROM entities WHERE user_id = ? AND delete_batch_id = ?)
		     OR target_id IN (SELECT id FROM entities WHERE user_id = ? AND delete_batch_id = ?))`,
		by, batchID, c.h.UserID, c.h.UserID, batchID, c.h.UserID, batchID)
	if err != nil {
		return "", 0, fmt.Errorf("cortex: soft delete relationships: %w", err)
	}
	total += rowsAffected(res)
	if total == 0 {
		return "", 0, nil
	}
	if err := tx.Commit(); err != nil {
		return "", 0, fmt.Errorf("cortex: commit soft delete: %w", err)
	}
	return batchID, total, nil
}

type batchChange struct {
	table, op, row string
}

// RestoreBatch reverses a batch of this user: undoes recorded merge changes
// (newest first), then un-deletes the batch's rows. An entity whose (name,
// type) is now taken by a live entity is merged into it instead.
func (c *Cortex) RestoreBatch(ctx context.Context, batchID string) error {
	tx, err := c.h.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cortex: begin transaction: %w", err)
	}
	defer tx.Rollback()

	var kind string
	err = tx.QueryRowContext(ctx,
		`SELECT kind FROM delete_batches WHERE id = ? AND user_id = ? AND restored_at IS NULL FOR UPDATE`,
		batchID, c.h.UserID).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return errBatchNotFound()
	}
	if err != nil {
		return fmt.Errorf("cortex: load delete batch: %w", err)
	}

	// 1. Undo recorded changes, newest first.
	rows, err := tx.QueryContext(ctx,
		`SELECT table_name, op, row_json::text FROM delete_batch_changes
		 WHERE batch_id = ? AND user_id = ? ORDER BY id DESC`, batchID, c.h.UserID)
	if err != nil {
		return fmt.Errorf("cortex: load batch changes: %w", err)
	}
	var changes []batchChange
	for rows.Next() {
		var ch batchChange
		if err := rows.Scan(&ch.table, &ch.op, &ch.row); err != nil {
			rows.Close()
			return fmt.Errorf("cortex: scan batch change: %w", err)
		}
		changes = append(changes, ch)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("cortex: iterate batch changes: %w", err)
	}
	for _, ch := range changes {
		if err := c.undoChange(ctx, tx, ch); err != nil {
			return err
		}
	}

	// 2. Un-delete the batch's non-entity rows. A batch edge whose twin was
	// re-asserted while it was deleted folds its evidence into the live row
	// and is dropped, since live edges are unique per (source, target, type).
	if _, err := tx.ExecContext(ctx,
		`UPDATE relationships l SET evidence_count = l.evidence_count + d.evidence_count
		 FROM relationships d
		 WHERE d.user_id = ? AND d.delete_batch_id = ? AND l.user_id = d.user_id AND l.deleted_at IS NULL
		   AND l.source_id = d.source_id AND l.target_id = d.target_id AND l.type = d.type`,
		c.h.UserID, batchID); err != nil {
		return fmt.Errorf("cortex: fold restored relationships: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM relationships d
		 WHERE d.user_id = ? AND d.delete_batch_id = ?
		   AND EXISTS (SELECT 1 FROM relationships l WHERE l.user_id = d.user_id AND l.deleted_at IS NULL
		               AND l.source_id = d.source_id AND l.target_id = d.target_id AND l.type = d.type)`,
		c.h.UserID, batchID); err != nil {
		return fmt.Errorf("cortex: drop folded relationships: %w", err)
	}
	for _, table := range []string{"relationships", "chunks", "memories"} {
		if _, err := tx.ExecContext(ctx,
			`UPDATE `+table+` SET deleted_at = NULL, deleted_by = NULL, delete_batch_id = NULL
			 WHERE user_id = ? AND delete_batch_id = ?`, c.h.UserID, batchID); err != nil {
			return fmt.Errorf("cortex: restore %s: %w", table, err)
		}
	}

	// 3. Entities, merging into a live namesake when one exists.
	erows, err := tx.QueryContext(ctx,
		`SELECT id FROM entities WHERE user_id = ? AND delete_batch_id = ? ORDER BY id`, c.h.UserID, batchID)
	if err != nil {
		return fmt.Errorf("cortex: load batch entities: %w", err)
	}
	var ids []string
	for erows.Next() {
		var id string
		if err := erows.Scan(&id); err != nil {
			erows.Close()
			return fmt.Errorf("cortex: scan batch entity: %w", err)
		}
		ids = append(ids, id)
	}
	erows.Close()
	if err := erows.Err(); err != nil {
		return fmt.Errorf("cortex: iterate batch entities: %w", err)
	}
	for _, id := range ids {
		var liveID string
		err := tx.QueryRowContext(ctx,
			`SELECT l.id FROM entities l JOIN entities d ON d.name = l.name AND d.type = l.type
			 WHERE d.id = ? AND l.user_id = ? AND l.deleted_at IS NULL AND l.id <> d.id`,
			id, c.h.UserID).Scan(&liveID)
		switch {
		case err == nil:
			if _, err := mergeEntitiesTx(ctx, tx, c.h.UserID, liveID, id, mergeOpts{}); err != nil {
				return fmt.Errorf("cortex: restore merge: %w", err)
			}
		case errors.Is(err, sql.ErrNoRows):
			if _, err := tx.ExecContext(ctx,
				`UPDATE entities SET deleted_at = NULL, deleted_by = NULL, delete_batch_id = NULL
				 WHERE id = ? AND user_id = ?`, id, c.h.UserID); err != nil {
				return fmt.Errorf("cortex: restore entity: %w", err)
			}
		default:
			return fmt.Errorf("cortex: check restore collision: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE delete_batches SET restored_at = now() WHERE id = ? AND user_id = ?`, batchID, c.h.UserID); err != nil {
		return fmt.Errorf("cortex: mark batch restored: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cortex: commit restore: %w", err)
	}
	return nil
}

// undoChange reverses one recorded merge change. Table names are checked
// against a fixed list before they reach SQL.
func (c *Cortex) undoChange(ctx context.Context, tx *store.Tx, ch batchChange) error {
	var q string
	var args []any
	switch ch.op {
	case "delete":
		switch ch.table {
		case "memory_entities", "relationships", "embeddings":
		default:
			return fmt.Errorf("cortex: unexpected batch table %q", ch.table)
		}
		q = `INSERT INTO ` + ch.table + ` SELECT r.* FROM jsonb_populate_record(NULL::` + ch.table + `, ?::jsonb) r ON CONFLICT DO NOTHING`
		args = []any{ch.row}
	case "retarget":
		switch ch.table {
		case "memory_entities":
			q = `UPDATE memory_entities t SET entity_id = r.entity_id
			     FROM jsonb_populate_record(NULL::memory_entities, ?::jsonb) r
			     WHERE t.user_id = ? AND t.memory_id = r.memory_id AND t.entity_id = (?::jsonb)->>'_to'`
			args = []any{ch.row, c.h.UserID, ch.row}
		case "relationships":
			q = `UPDATE relationships t SET source_id = r.source_id, target_id = r.target_id
			     FROM jsonb_populate_record(NULL::relationships, ?::jsonb) r
			     WHERE t.id = r.id AND t.user_id = ?`
			args = []any{ch.row, c.h.UserID}
		case "entity_aliases":
			q = `UPDATE entity_aliases t SET entity_id = r.entity_id
			     FROM jsonb_populate_record(NULL::entity_aliases, ?::jsonb) r
			     WHERE t.user_id = ? AND t.kind = r.kind AND t.value_norm = r.value_norm`
			args = []any{ch.row, c.h.UserID}
		case "chunks":
			q = `UPDATE chunks t SET entity_id = r.entity_id
			     FROM jsonb_populate_record(NULL::chunks, ?::jsonb) r
			     WHERE t.id = r.id AND t.user_id = ?`
			args = []any{ch.row, c.h.UserID}
		default:
			return fmt.Errorf("cortex: unexpected batch table %q", ch.table)
		}
	case "insert":
		if ch.table != "entity_aliases" {
			return fmt.Errorf("cortex: unexpected batch table %q", ch.table)
		}
		q = `DELETE FROM entity_aliases t
		     USING jsonb_populate_record(NULL::entity_aliases, ?::jsonb) r
		     WHERE t.user_id = ? AND t.kind = r.kind AND t.value_norm = r.value_norm AND t.entity_id = r.entity_id`
		args = []any{ch.row, c.h.UserID}
	case "update":
		switch ch.table {
		case "entities":
			q = `UPDATE entities t SET attributes = r.attributes, updated_at = r.updated_at, type = r.type, name = r.name
			     FROM jsonb_populate_record(NULL::entities, ?::jsonb) r
			     WHERE t.id = r.id AND t.user_id = ?`
		case "relationships":
			q = `UPDATE relationships t SET type = r.type, source_id = r.source_id, target_id = r.target_id,
			       attributes = r.attributes, evidence_count = r.evidence_count
			     FROM jsonb_populate_record(NULL::relationships, ?::jsonb) r
			     WHERE t.id = r.id AND t.user_id = ?`
		default:
			return fmt.Errorf("cortex: unexpected batch table %q", ch.table)
		}
		args = []any{ch.row, c.h.UserID}
	default:
		return fmt.Errorf("cortex: unexpected batch op %q", ch.op)
	}
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("cortex: undo %s %s: %w", ch.table, ch.op, err)
	}
	return nil
}

// ListDeleted returns this user's soft-deleted memories, newest deletion first.
func (c *Cortex) ListDeleted(ctx context.Context, limit, offset int) ([]Memory, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := c.h.DB.QueryContext(ctx,
		`SELECT id, content, source, speaker, confidence, created_at, updated_at
		 FROM memories WHERE user_id = ? AND deleted_at IS NOT NULL
		 ORDER BY deleted_at DESC, id LIMIT ? OFFSET ?`, c.h.UserID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("cortex: list deleted: %w", err)
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		var m Memory
		var src, spk sql.NullString
		if err := rows.Scan(&m.ID, &m.Content, &src, &spk, &m.Confidence, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("cortex: scan deleted memory: %w", err)
		}
		m.Source, m.Speaker = src.String, spk.String
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListDeleteBatches lists this user's unrestored batches, newest first.
func (c *Cortex) ListDeleteBatches(ctx context.Context) ([]DeleteBatch, error) {
	rows, err := c.h.DB.QueryContext(ctx,
		`SELECT b.id, b.kind, b.created_at,
		   (SELECT count(*) FROM memories      WHERE delete_batch_id = b.id AND user_id = b.user_id),
		   (SELECT count(*) FROM entities      WHERE delete_batch_id = b.id AND user_id = b.user_id),
		   (SELECT count(*) FROM relationships WHERE delete_batch_id = b.id AND user_id = b.user_id),
		   (SELECT count(*) FROM chunks        WHERE delete_batch_id = b.id AND user_id = b.user_id)
		 FROM delete_batches b WHERE b.user_id = ? AND b.restored_at IS NULL
		 ORDER BY b.created_at DESC, b.id DESC`, c.h.UserID)
	if err != nil {
		return nil, fmt.Errorf("cortex: list delete batches: %w", err)
	}
	defer rows.Close()
	var out []DeleteBatch
	for rows.Next() {
		var b DeleteBatch
		if err := rows.Scan(&b.ID, &b.Kind, &b.CreatedAt, &b.Memories, &b.Entities, &b.Relationships, &b.Chunks); err != nil {
			return nil, fmt.Errorf("cortex: scan delete batch: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// PurgeDeleted hard-deletes this user's rows soft-deleted more than olderThan
// ago, with their embeddings (every generation), memory links and memory
// edges, in one transaction. Batches older than the cutoff, or already
// restored, are removed with their change records. It returns the number of
// memories, entities, relationships and chunks removed.
func (c *Cortex) PurgeDeleted(ctx context.Context, olderThan time.Duration) (int, error) {
	cutoff := time.Now().UTC().Add(-olderThan)
	tx, err := c.h.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("cortex: begin transaction: %w", err)
	}
	defer tx.Rollback()

	exec := func(what, q string, args ...any) (int, error) {
		res, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			return 0, fmt.Errorf("cortex: purge %s: %w", what, err)
		}
		return rowsAffected(res), nil
	}
	u := c.h.UserID
	total := 0

	memSel := `SELECT id FROM memories WHERE user_id = ? AND deleted_at IS NOT NULL AND deleted_at < ?`
	entSel := `SELECT id FROM entities WHERE user_id = ? AND deleted_at IS NOT NULL AND deleted_at < ?`
	chSel := `SELECT id FROM chunks WHERE user_id = ? AND deleted_at IS NOT NULL AND deleted_at < ?`

	steps := []struct {
		what, q string
		args    []any
		count   bool
	}{
		{"memory embeddings", `DELETE FROM embeddings WHERE user_id = ? AND ref_type = 'memory' AND ref_id IN (` + memSel + `)`, []any{u, u, cutoff}, false},
		{"memory links", `DELETE FROM memory_entities WHERE user_id = ? AND memory_id IN (` + memSel + `)`, []any{u, u, cutoff}, false},
		{"memory edges", `DELETE FROM memory_edges WHERE user_id = ? AND (source_id IN (` + memSel + `) OR target_id IN (` + memSel + `))`, []any{u, u, cutoff, u, cutoff}, false},
		{"memories", `DELETE FROM memories WHERE user_id = ? AND deleted_at IS NOT NULL AND deleted_at < ?`, []any{u, cutoff}, true},
		{"chunk embeddings", `DELETE FROM embeddings WHERE user_id = ? AND ref_type = 'chunk' AND ref_id IN (` + chSel + `)`, []any{u, u, cutoff}, false},
		{"chunks", `DELETE FROM chunks WHERE user_id = ? AND deleted_at IS NOT NULL AND deleted_at < ?`, []any{u, cutoff}, true},
		{"entity embeddings", `DELETE FROM embeddings WHERE user_id = ? AND ref_type = 'entity' AND ref_id IN (` + entSel + `)`, []any{u, u, cutoff}, false},
		{"entity links", `DELETE FROM memory_entities WHERE user_id = ? AND entity_id IN (` + entSel + `)`, []any{u, u, cutoff}, false},
		{"relationships", `DELETE FROM relationships WHERE user_id = ? AND ((deleted_at IS NOT NULL AND deleted_at < ?)
			OR source_id IN (` + entSel + `) OR target_id IN (` + entSel + `))`, []any{u, cutoff, u, cutoff, u, cutoff}, true},
		{"entities", `DELETE FROM entities WHERE user_id = ? AND deleted_at IS NOT NULL AND deleted_at < ?`, []any{u, cutoff}, true},
		{"batches", `DELETE FROM delete_batches WHERE user_id = ? AND (created_at < ? OR restored_at IS NOT NULL)`, []any{u, cutoff}, false},
	}
	for _, s := range steps {
		n, err := exec(s.what, s.q, s.args...)
		if err != nil {
			return 0, err
		}
		if s.count {
			total += n
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("cortex: commit purge: %w", err)
	}
	return total, nil
}

// SoftDeleteChunks marks the chunks with the given IDs as deleted in one
// batch. The batch ID is empty when nothing matched.
func (c *Cortex) SoftDeleteChunks(ctx context.Context, ids []string, by string) (string, int, error) {
	if len(ids) == 0 {
		return "", 0, fmt.Errorf("cortex: no chunk ids given")
	}
	ph := make([]string, len(ids))
	args := []any{c.h.UserID}
	for i, id := range ids {
		ph[i] = "?"
		args = append(args, id)
	}
	tx, err := c.h.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, fmt.Errorf("cortex: begin transaction: %w", err)
	}
	defer tx.Rollback()
	batchID, err := newBatch(ctx, tx, c.h.UserID, "chunks")
	if err != nil {
		return "", 0, err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE chunks SET deleted_at = now(), deleted_by = ?, delete_batch_id = ?
		 WHERE user_id = ? AND deleted_at IS NULL AND id IN (`+strings.Join(ph, ",")+`)`,
		append([]any{by, batchID}, args...)...)
	if err != nil {
		return "", 0, fmt.Errorf("cortex: soft delete chunks: %w", err)
	}
	n := rowsAffected(res)
	if n == 0 {
		return "", 0, nil
	}
	if err := tx.Commit(); err != nil {
		return "", 0, fmt.Errorf("cortex: commit soft delete: %w", err)
	}
	return batchID, n, nil
}
