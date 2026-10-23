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

// Usage is a user's storage footprint.
type Usage struct {
	Bytes    int64 `json:"bytes"`
	Memories int   `json:"memories"`
	Entities int   `json:"entities"`
	Chunks   int   `json:"chunks"`
	Deleted  int   `json:"deleted"`
}

// ComputeUsage measures this user's rows now. Bytes is the sum of
// pg_column_size over every row the user owns, including embeddings of all
// generations and soft-deleted rows (they still occupy space until purged).
func (c *Cortex) ComputeUsage(ctx context.Context) (Usage, error) {
	var u Usage
	for _, t := range []string{"entities", "relationships", "memories", "memory_entities", "memory_edges", "chunks", "embeddings"} {
		var n sql.NullInt64
		if err := c.db.QueryRowContext(ctx,
			`SELECT sum(pg_column_size(t.*)) FROM `+t+` t WHERE t.user_id = ?`, c.userID).Scan(&n); err != nil {
			return u, fmt.Errorf("cortex: usage bytes (%s): %w", t, err)
		}
		u.Bytes += n.Int64
	}
	err := c.db.QueryRowContext(ctx,
		`SELECT
		   (SELECT count(*) FROM memories WHERE user_id = ? AND deleted_at IS NULL),
		   (SELECT count(*) FROM entities WHERE user_id = ? AND deleted_at IS NULL),
		   (SELECT count(*) FROM chunks   WHERE user_id = ? AND deleted_at IS NULL),
		   (SELECT count(*) FROM memories WHERE user_id = ? AND deleted_at IS NOT NULL)
		 + (SELECT count(*) FROM entities WHERE user_id = ? AND deleted_at IS NOT NULL)
		 + (SELECT count(*) FROM chunks   WHERE user_id = ? AND deleted_at IS NOT NULL)`,
		c.userID, c.userID, c.userID, c.userID, c.userID, c.userID).
		Scan(&u.Memories, &u.Entities, &u.Chunks, &u.Deleted)
	if err != nil {
		return u, fmt.Errorf("cortex: usage counts: %w", err)
	}
	return u, nil
}

// RefreshUsage computes the usage and stores it in users.usage_json (the
// nightly job calls this).
func (c *Cortex) RefreshUsage(ctx context.Context) (Usage, error) {
	u, err := c.ComputeUsage(ctx)
	if err != nil {
		return u, err
	}
	b, _ := json.Marshal(u)
	if _, err := c.db.ExecContext(ctx, `UPDATE users SET usage_json = ?::jsonb WHERE id = ?`, string(b), c.userID); err != nil {
		return u, fmt.Errorf("cortex: store usage: %w", err)
	}
	return u, nil
}

// Usage returns the cached figures, or computes them live when none are cached.
func (c *Cortex) Usage(ctx context.Context) (Usage, error) {
	var raw sql.NullString
	err := c.db.QueryRowContext(ctx, `SELECT usage_json::text FROM users WHERE id = ?`, c.userID).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Usage{}, fmt.Errorf("cortex: read usage: %w", err)
	}
	if raw.Valid {
		var u Usage
		if json.Unmarshal([]byte(raw.String), &u) == nil {
			return u, nil
		}
	}
	return c.ComputeUsage(ctx)
}

// MemoryListFilter narrows ListMemories. Zero fields are ignored.
type MemoryListFilter struct {
	Query     string
	WrittenBy string
	// EntityID keeps memories linked to this entity (a foreign ID matches none).
	EntityID string
	From, To *time.Time // created_at in [From, To)
	Oldest   bool       // oldest first; the default is newest first. Ties break on id.
}

// ListMemories pages this user's live memories, newest first unless
// f.Oldest, and returns the
// total number matching the filter.
func (c *Cortex) ListMemories(ctx context.Context, f MemoryListFilter, limit, offset int) ([]Memory, int, error) {
	conds := []string{"user_id = ?", "deleted_at IS NULL"}
	args := []any{c.userID}
	if f.Query != "" {
		esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(f.Query)
		conds = append(conds, `content ILIKE ? ESCAPE '\'`)
		args = append(args, "%"+esc+"%")
	}
	if f.WrittenBy != "" {
		conds = append(conds, "written_by_connection_id = ?")
		args = append(args, f.WrittenBy)
	}
	if f.EntityID != "" {
		conds = append(conds, "id IN (SELECT memory_id FROM memory_entities WHERE user_id = ? AND entity_id = ?)")
		args = append(args, c.userID, f.EntityID)
	}
	if f.From != nil {
		conds = append(conds, "created_at >= ?")
		args = append(args, *f.From)
	}
	if f.To != nil {
		conds = append(conds, "created_at < ?")
		args = append(args, *f.To)
	}
	where := strings.Join(conds, " AND ")
	var total int
	if err := c.db.QueryRowContext(ctx, `SELECT count(*) FROM memories WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("cortex: count memories: %w", err)
	}
	if limit <= 0 {
		limit = 50
	}
	dir := "DESC"
	if f.Oldest {
		dir = "ASC"
	}
	rows, err := c.db.QueryContext(ctx,
		`SELECT id, content, COALESCE(source,''), COALESCE(speaker,''), COALESCE(written_by_connection_id,''), confidence, created_at, updated_at
		 FROM memories WHERE `+where+` ORDER BY created_at `+dir+`, id LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("cortex: list memories: %w", err)
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.Content, &m.Source, &m.Speaker, &m.WrittenBy, &m.Confidence, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, 0, fmt.Errorf("cortex: scan memory: %w", err)
		}
		out = append(out, m)
	}
	return out, total, rows.Err()
}
