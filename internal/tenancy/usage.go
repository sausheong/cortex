package tenancy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sausheong/cortex/internal/model"
)

// ComputeUsage measures this user's rows now. Bytes is the sum of
// pg_column_size over every row the user owns, including embeddings of all
// generations and soft-deleted rows (they still occupy space until purged).
func (c Ops) ComputeUsage(ctx context.Context) (model.Usage, error) {
	var u model.Usage
	for _, t := range []string{"entities", "relationships", "memories", "memory_entities", "memory_edges", "entity_aliases", "chunks", "embeddings"} {
		var n sql.NullInt64
		if err := c.DB.QueryRowContext(ctx,
			`SELECT sum(pg_column_size(t.*)) FROM `+t+` t WHERE t.user_id = ?`, c.UserID).Scan(&n); err != nil {
			return u, fmt.Errorf("cortex: usage bytes (%s): %w", t, err)
		}
		u.Bytes += n.Int64
	}
	err := c.DB.QueryRowContext(ctx,
		`SELECT
		   (SELECT count(*) FROM memories WHERE user_id = ? AND deleted_at IS NULL),
		   (SELECT count(*) FROM entities WHERE user_id = ? AND deleted_at IS NULL),
		   (SELECT count(*) FROM chunks   WHERE user_id = ? AND deleted_at IS NULL),
		   (SELECT count(*) FROM memories WHERE user_id = ? AND deleted_at IS NOT NULL)
		 + (SELECT count(*) FROM entities WHERE user_id = ? AND deleted_at IS NOT NULL)
		 + (SELECT count(*) FROM chunks   WHERE user_id = ? AND deleted_at IS NOT NULL)`,
		c.UserID, c.UserID, c.UserID, c.UserID, c.UserID, c.UserID).
		Scan(&u.Memories, &u.Entities, &u.Chunks, &u.Deleted)
	if err != nil {
		return u, fmt.Errorf("cortex: usage counts: %w", err)
	}
	return u, nil
}

type usageCounts struct {
	Memories int `json:"memories"`
	Entities int `json:"entities"`
	Chunks   int `json:"chunks"`
	Deleted  int `json:"deleted"`
}

type usageDoc struct {
	Bytes  int64       `json:"bytes"`
	Counts usageCounts `json:"counts"`
}

// RefreshUsage computes the usage and stores it in users.usage_json (the
// nightly job calls this).
func (c Ops) RefreshUsage(ctx context.Context) (model.Usage, error) {
	u, err := c.ComputeUsage(ctx)
	if err != nil {
		return u, err
	}
	// The plain part is numeric only: no key or value names content. Anything
	// that would (none today) belongs in the sealed usage_ct.
	b, _ := json.Marshal(usageDoc{Bytes: u.Bytes, Counts: usageCounts{Memories: u.Memories, Entities: u.Entities, Chunks: u.Chunks, Deleted: u.Deleted}})
	if _, err := c.DB.ExecContext(ctx, `UPDATE users SET usage_json = ?::jsonb WHERE id = ?`, string(b), c.UserID); err != nil {
		return u, fmt.Errorf("cortex: store usage: %w", err)
	}
	return u, nil
}

// CachedUsage returns the figures stored by the nightly job. ok is false
// when none are cached yet; it never measures, so it is cheap on a page load.
func (c Ops) CachedUsage(ctx context.Context) (u model.Usage, ok bool, err error) {
	var raw sql.NullString
	err = c.DB.QueryRowContext(ctx, `SELECT usage_json::text FROM users WHERE id = ?`, c.UserID).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return model.Usage{}, false, fmt.Errorf("cortex: read usage: %w", err)
	}
	if raw.Valid {
		// {"bytes":..,"counts":{..}}, or the flat form written before encryption.
		var d usageDoc
		if strings.Contains(raw.String, `"counts"`) && json.Unmarshal([]byte(raw.String), &d) == nil {
			return model.Usage{Bytes: d.Bytes, Memories: d.Counts.Memories, Entities: d.Counts.Entities,
				Chunks: d.Counts.Chunks, Deleted: d.Counts.Deleted}, true, nil
		}
		if json.Unmarshal([]byte(raw.String), &u) == nil {
			return u, true, nil
		}
	}
	return model.Usage{}, false, nil
}

// Usage returns the cached figures, or computes them live when none are cached.
func (c Ops) Usage(ctx context.Context) (model.Usage, error) {
	if u, ok, err := c.CachedUsage(ctx); err != nil || ok {
		return u, err
	}
	return c.ComputeUsage(ctx)
}

// NewMemoriesSince groups this user's live memories created after since by
// the connection that wrote them, with each group's newest perGroup
// memories. Groups are ordered by their newest memory, newest first. It is
// one indexed query and never calls a model.
func (c Ops) NewMemoriesSince(ctx context.Context, since time.Time, perGroup int) ([]model.ConnectionNew, error) {
	if perGroup < 1 {
		perGroup = 3
	}
	return c.newMemoriesSinceEnc(ctx, since, perGroup)
}

// WrittenSince counts this user's live memories created after since, per
// writing connection ID ("" for none).
func (c Ops) WrittenSince(ctx context.Context, since time.Time) (map[string]int, error) {
	rows, err := c.DB.QueryContext(ctx,
		`SELECT COALESCE(written_by_connection_id,''), count(*) FROM memories
		 WHERE user_id = ? AND deleted_at IS NULL AND created_at > ? GROUP BY 1`, c.UserID, since)
	if err != nil {
		return nil, fmt.Errorf("cortex: written since: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// ListMemories pages this user's live memories, newest first unless
// f.Oldest, and returns the
// total number matching the filter.
func (c Ops) ListMemories(ctx context.Context, f model.MemoryListFilter, limit, offset int) ([]model.Memory, int, error) {
	return c.listMemoriesEnc(ctx, f, limit, offset)
}
