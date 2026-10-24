package cortex

import (
	"context"
	"database/sql"
	"fmt"
)

// maxReembedChars caps the text sent to an embedder for one row.
const maxReembedChars = 8000

type rowsQuerier interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

func truncateEmbedText(s string) string {
	r := []rune(s)
	if len(r) <= maxReembedChars {
		return s
	}
	return string(r[:maxReembedChars])
}

var reembedKinds = []struct{ refType, table, col string }{
	{"entity", "entities", "name"},
	{"memory", "memories", "content"},
	{"chunk", "chunks", "content"},
}

// PendingEmbeddings returns up to limit rows (soft-deleted ones included)
// of this handle's user that have no embedding at generation gen, with the
// text to embed for each.
func (c *Cortex) PendingEmbeddings(ctx context.Context, gen, limit int) ([]EmbedItem, error) {
	return pendingEmbeddings(ctx, c.db.DB, c.userID, gen, limit)
}

// PendingEmbeddingsTx is PendingEmbeddings inside an open transaction.
func (c *Cortex) PendingEmbeddingsTx(ctx context.Context, tx *sql.Tx, gen, limit int) ([]EmbedItem, error) {
	return pendingEmbeddings(ctx, tx, c.userID, gen, limit)
}

func pendingEmbeddings(ctx context.Context, q rowsQuerier, userID string, gen, limit int) ([]EmbedItem, error) {
	var out []EmbedItem
	for _, k := range reembedKinds {
		if len(out) >= limit {
			break
		}
		rows, err := q.QueryContext(ctx, fmt.Sprintf(
			`SELECT t.id, t.%s FROM %s t WHERE t.user_id = $1
			 AND NOT EXISTS (SELECT 1 FROM embeddings e WHERE e.user_id = t.user_id
			   AND e.ref_type = $2 AND e.ref_id = t.id AND e.generation = $3)
			 ORDER BY t.id LIMIT $4`, k.col, k.table), userID, k.refType, gen, limit-len(out))
		if err != nil {
			return nil, fmt.Errorf("cortex: pending embeddings: %w", err)
		}
		start := len(out)
		for rows.Next() {
			var it EmbedItem
			if err := rows.Scan(&it.ID, &it.Text); err != nil {
				rows.Close()
				return nil, fmt.Errorf("cortex: scan pending embedding: %w", err)
			}
			it.RefType = k.refType
			out = append(out, it)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("cortex: pending embeddings: %w", err)
		}
		if k.refType == "memory" && len(out) > start {
			if err := fillMemoryContext(ctx, q, userID, out[start:]); err != nil {
				return nil, err
			}
		}
		for i := start; i < len(out); i++ {
			out[i].Text = truncateEmbedText(out[i].Text)
		}
	}
	return out, nil
}

// fillMemoryContext turns memory items' text into the fact-augmented key.
func fillMemoryContext(ctx context.Context, q rowsQuerier, userID string, items []EmbedItem) error {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	rows, err := q.QueryContext(ctx,
		`SELECT me.memory_id, e.name FROM memory_entities me
		 JOIN entities e ON e.id = me.entity_id AND e.user_id = me.user_id
		 WHERE me.user_id = $1 AND me.memory_id = ANY($2)`, userID, ids)
	if err != nil {
		return fmt.Errorf("cortex: memory entity names: %w", err)
	}
	defer rows.Close()
	names := map[string][]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return fmt.Errorf("cortex: scan entity name: %w", err)
		}
		names[id] = append(names[id], name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range items {
		items[i].Text = augmentForEmbedding(items[i].Text, names[items[i].ID])
	}
	return nil
}

// CountPendingEmbeddings counts rows (soft-deleted included) lacking an
// embedding at generation gen.
func (c *Cortex) CountPendingEmbeddings(ctx context.Context, gen int) (int, error) {
	return c.countEmbeddable(ctx, gen, true)
}

// CountEmbeddable counts all rows that carry an embedding.
func (c *Cortex) CountEmbeddable(ctx context.Context) (int, error) {
	return c.countEmbeddable(ctx, 0, false)
}

func (c *Cortex) countEmbeddable(ctx context.Context, gen int, pendingOnly bool) (int, error) {
	total := 0
	for _, k := range reembedKinds {
		var n int
		q := fmt.Sprintf(`SELECT count(*) FROM %s t WHERE t.user_id = $1`, k.table)
		args := []any{c.userID}
		if pendingOnly {
			q += ` AND NOT EXISTS (SELECT 1 FROM embeddings e WHERE e.user_id = t.user_id
				AND e.ref_type = $2 AND e.ref_id = t.id AND e.generation = $3)`
			args = append(args, k.refType, gen)
		}
		if err := c.db.DB.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			return 0, fmt.Errorf("cortex: count embeddable: %w", err)
		}
		total += n
	}
	return total, nil
}
