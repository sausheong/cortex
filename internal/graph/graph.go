package graph

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
	"github.com/sausheong/cortex/internal/tenancy"
)

// GraphNeighbourhood returns the centre entity with up to Limit-1 of its
// direct neighbours, ranked by how many relationships join them to the centre
// and then by recency, and the live relationships among the chosen nodes. An
// absent or foreign centre is ErrNotFound. It runs two queries after the
// centre lookup, both using the relationship source/target indexes.
func (c Ops) GraphNeighbourhood(ctx context.Context, centerID string, f model.GraphFilter) (*model.Neighbourhood, error) {
	center, err := c.GetEntity(ctx, centerID)
	if err != nil {
		return nil, err
	}
	limit := f.Limit
	if limit < 1 {
		limit = 60
	}
	if limit > 500 {
		limit = 500
	}
	g := &model.Neighbourhood{Nodes: []model.GraphNode{{ID: center.ID, Type: center.Type, Name: center.Name, Center: true}}}

	where := ""
	args := []any{centerID, c.UserID, centerID, centerID, c.UserID, centerID}
	if len(f.Types) > 0 {
		where += " AND e.type = ANY(?)"
		args = append(args, f.Types)
	}
	if f.Since != nil {
		where += ` AND EXISTS (SELECT 1 FROM memory_entities me JOIN memories m ON m.id = me.memory_id AND m.user_id = me.user_id
			WHERE me.user_id = e.user_id AND me.entity_id = e.id AND m.deleted_at IS NULL AND m.created_at >= ?)`
		args = append(args, *f.Since)
	}
	// The limit counts the centre, so fetching `limit` rows is one more than
	// fits, which is how truncation is detected.
	args = append(args, limit)
	rows, err := c.DB.QueryContext(ctx,
		`SELECT e.id, e.type, e.name, count(*) AS rels
		 FROM (SELECT CASE WHEN source_id = ? THEN target_id ELSE source_id END AS nid, created_at
		       FROM relationships WHERE user_id = ? AND deleted_at IS NULL AND (source_id = ? OR target_id = ?)) inc
		 JOIN entities e ON e.id = inc.nid AND e.user_id = ? AND e.deleted_at IS NULL
		 WHERE e.id <> ?`+where+`
		 GROUP BY e.id, e.type, e.name
		 ORDER BY rels DESC, max(inc.created_at) DESC, e.name, e.id
		 LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("cortex: graph neighbours: %w", err)
	}
	defer rows.Close()
	ids := []string{centerID}
	for rows.Next() {
		var n model.GraphNode
		if err := rows.Scan(&n.ID, &n.Type, &n.Name, &n.Rels); err != nil {
			return nil, fmt.Errorf("cortex: scan graph node: %w", err)
		}
		if len(g.Nodes) >= limit {
			g.Truncated = true
			break
		}
		g.Nodes = append(g.Nodes, n)
		ids = append(ids, n.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cortex: graph neighbours: %w", err)
	}
	rows.Close()

	erows, err := c.DB.QueryContext(ctx,
		`SELECT id, source_id, target_id, type FROM relationships
		 WHERE user_id = ? AND deleted_at IS NULL AND source_id = ANY(?) AND target_id = ANY(?)
		 ORDER BY created_at DESC, id LIMIT ?`, c.UserID, ids, ids, limit*6)
	if err != nil {
		return nil, fmt.Errorf("cortex: graph edges: %w", err)
	}
	defer erows.Close()
	for erows.Next() {
		var e model.GraphEdge
		if err := erows.Scan(&e.ID, &e.SourceID, &e.TargetID, &e.Type); err != nil {
			return nil, fmt.Errorf("cortex: scan graph edge: %w", err)
		}
		g.Edges = append(g.Edges, e)
	}
	return g, erows.Err()
}

// RenameEntity renames a live entity of this user. A name already used by
// another live entity of the same type returns *ErrEntityNameTaken. The
// entity's name embedding is dropped in the same transaction because it
// described the old name; re-embedding rebuilds it.
func (c Ops) RenameEntity(ctx context.Context, id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("cortex: entity name is empty")
	}
	if err := c.RequireKey(); err != nil {
		return err
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cortex: begin rename: %w", err)
	}
	defer tx.Rollback()
	if c.Encrypted() {
		return c.renameEntityEnc(ctx, tx, id, name)
	}
	var typ string
	err = tx.QueryRowContext(ctx, `SELECT type FROM entities WHERE id = ? AND user_id = ? AND deleted_at IS NULL FOR UPDATE`, id, c.UserID).Scan(&typ)
	if err != nil {
		return store.ErrEntityNotFound()
	}
	var other string
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM entities WHERE user_id = ? AND name = ? AND type = ? AND deleted_at IS NULL AND id <> ?`,
		c.UserID, name, typ, id).Scan(&other)
	if err == nil {
		return &model.ErrEntityNameTaken{ConflictID: other}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE entities SET name = ?, updated_at = now() WHERE id = ? AND user_id = ?`, name, id, c.UserID); err != nil {
		return fmt.Errorf("cortex: rename entity: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM embeddings WHERE user_id = ? AND ref_type = 'entity' AND ref_id = ?`, c.UserID, id); err != nil {
		return fmt.Errorf("cortex: drop stale entity embedding: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cortex: commit rename: %w", err)
	}
	return nil
}

// SearchEntities finds live entities whose name contains q (case-insensitive,
// wildcards in q are literal), names starting with q first. Types, when given,
// restricts the result; ExcludeID drops one entity (used to pick a merge
// target). Results are capped at limit (default 20, max 100).
func (c Ops) SearchEntities(ctx context.Context, q string, types []string, excludeID string, limit int) ([]model.Entity, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
	conds := `user_id = ? AND deleted_at IS NULL AND name ILIKE ? ESCAPE '\'`
	args := []any{c.UserID, "%" + esc + "%"}
	if len(types) > 0 {
		conds += ` AND type = ANY(?)`
		args = append(args, types)
	}
	if excludeID != "" {
		conds += ` AND id <> ?`
		args = append(args, excludeID)
	}
	args = append(args, esc+"%", limit)
	rows, err := c.DB.QueryContext(ctx,
		`SELECT id, type, name, source FROM entities WHERE `+conds+`
		 ORDER BY (name ILIKE ? ESCAPE '\') DESC, name, id LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("cortex: search entities: %w", err)
	}
	defer rows.Close()
	var out []model.Entity
	for rows.Next() {
		var e model.Entity
		var src *string
		if err := rows.Scan(&e.ID, &e.Type, &e.Name, &src); err != nil {
			return nil, fmt.Errorf("cortex: scan entity: %w", err)
		}
		if src != nil {
			e.Source = *src
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EntityTypes lists the distinct types of this user's live entities.
func (c Ops) EntityTypes(ctx context.Context) ([]string, error) {
	rows, err := c.DB.QueryContext(ctx, `SELECT DISTINCT type FROM entities WHERE user_id = ? AND deleted_at IS NULL ORDER BY type`, c.UserID)
	if err != nil {
		return nil, fmt.Errorf("cortex: entity types: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// EntityRelations returns up to limit live relationships touching the entity
// (newest first) and the total count. Relationships whose other end is gone
// are not counted.
func (c Ops) EntityRelations(ctx context.Context, id string, limit int) ([]model.EntityRelation, int, error) {
	if limit < 1 {
		limit = 50
	}
	var total int
	if err := c.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM relationships r
		 JOIN entities o ON o.user_id = r.user_id AND o.deleted_at IS NULL AND o.id = CASE WHEN r.source_id = ? THEN r.target_id ELSE r.source_id END
		 WHERE r.user_id = ? AND r.deleted_at IS NULL AND (r.source_id = ? OR r.target_id = ?)`,
		id, c.UserID, id, id).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("cortex: count relations: %w", err)
	}
	rows, err := c.DB.QueryContext(ctx,
		`SELECT r.id, r.type, r.source_id = ?, o.id, o.name, o.type, r.created_at FROM relationships r
		 JOIN entities o ON o.user_id = r.user_id AND o.deleted_at IS NULL AND o.id = CASE WHEN r.source_id = ? THEN r.target_id ELSE r.source_id END
		 WHERE r.user_id = ? AND r.deleted_at IS NULL AND (r.source_id = ? OR r.target_id = ?)
		 ORDER BY r.created_at DESC, r.id LIMIT ?`,
		id, id, c.UserID, id, id, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("cortex: entity relations: %w", err)
	}
	defer rows.Close()
	var out []model.EntityRelation
	for rows.Next() {
		var r model.EntityRelation
		if err := rows.Scan(&r.ID, &r.Type, &r.Outgoing, &r.OtherID, &r.OtherName, &r.OtherType, &r.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// EntityMentions returns the latest live memories linked to the entity and
// the total number of them.
func (c Ops) EntityMentions(ctx context.Context, id string, limit int) ([]model.Memory, int, error) {
	return tenancy.New(c.Handle).ListMemories(ctx, model.MemoryListFilter{EntityID: id}, limit, 0)
}
