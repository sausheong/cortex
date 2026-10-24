package memory

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
)

// maxReembedChars caps the text sent to an embedder for one row.
const maxReembedChars = 8000

type rowsQuerier interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

func TruncateEmbedText(s string) string {
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
func (c Ops) PendingEmbeddings(ctx context.Context, gen, limit int) ([]model.EmbedItem, error) {
	return c.pendingEmbeddings(ctx, c.DB.DB, gen, limit)
}

// PendingEmbeddingsTx is PendingEmbeddings inside an open transaction.
func (c Ops) PendingEmbeddingsTx(ctx context.Context, tx *sql.Tx, gen, limit int) ([]model.EmbedItem, error) {
	return c.pendingEmbeddings(ctx, tx, gen, limit)
}

func (c Ops) pendingEmbeddings(ctx context.Context, q rowsQuerier, gen, limit int) ([]model.EmbedItem, error) {
	userID := c.UserID
	if c.Encrypted() {
		return c.pendingEmbeddingsEnc(ctx, q, gen, limit)
	}
	var out []model.EmbedItem
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
			var it model.EmbedItem
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
			out[i].Text = TruncateEmbedText(out[i].Text)
		}
	}
	return out, nil
}

// fillMemoryContext turns memory items' text into the fact-augmented key.
func fillMemoryContext(ctx context.Context, q rowsQuerier, userID string, items []model.EmbedItem) error {
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
		items[i].Text = AugmentForEmbedding(items[i].Text, names[items[i].ID])
	}
	return nil
}

// CountPendingEmbeddings counts rows (soft-deleted included) lacking an
// embedding at generation gen.
func (c Ops) CountPendingEmbeddings(ctx context.Context, gen int) (int, error) {
	return c.countEmbeddable(ctx, gen, true)
}

// CountEmbeddable counts all rows that carry an embedding.
func (c Ops) CountEmbeddable(ctx context.Context) (int, error) {
	return c.countEmbeddable(ctx, 0, false)
}

func (c Ops) countEmbeddable(ctx context.Context, gen int, pendingOnly bool) (int, error) {
	total := 0
	for _, k := range reembedKinds {
		var n int
		q := fmt.Sprintf(`SELECT count(*) FROM %s t WHERE t.user_id = $1`, k.table)
		args := []any{c.UserID}
		if pendingOnly {
			q += ` AND NOT EXISTS (SELECT 1 FROM embeddings e WHERE e.user_id = t.user_id
				AND e.ref_type = $2 AND e.ref_id = t.id AND e.generation = $3)`
			args = append(args, k.refType, gen)
		}
		if err := c.DB.DB.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			return 0, fmt.Errorf("cortex: count embeddable: %w", err)
		}
		total += n
	}
	return total, nil
}

var reembedTables = map[string]string{"entities": store.TblEntities, "memories": store.TblMemories, "chunks": store.TblChunks}

// pendingEmbeddingsEnc is pendingEmbeddings for encrypted (and migrating)
// users: the text of each row is decrypted (or read, for a legacy row of a
// migrating user) here, since Postgres only holds ciphertext.
func (c Ops) pendingEmbeddingsEnc(ctx context.Context, q rowsQuerier, gen, limit int) ([]model.EmbedItem, error) {
	if err := c.RequireKey(); err != nil {
		return nil, err
	}
	var out []model.EmbedItem
	for _, k := range reembedKinds {
		if len(out) >= limit {
			break
		}
		rows, err := q.QueryContext(ctx, fmt.Sprintf(
			`SELECT t.id, t.enc, t.%s, t.%s_ct FROM %s t WHERE t.user_id = $1
			 AND NOT EXISTS (SELECT 1 FROM embeddings e WHERE e.user_id = t.user_id
			   AND e.ref_type = $2 AND e.ref_id = t.id AND e.generation = $3)
			 ORDER BY t.id LIMIT $4`, k.col, k.col, k.table), c.UserID, k.refType, gen, limit-len(out))
		if err != nil {
			return nil, fmt.Errorf("cortex: pending embeddings: %w", err)
		}
		start := len(out)
		for rows.Next() {
			var it model.EmbedItem
			var enc bool
			var plain sql.NullString
			var ct []byte
			if err := rows.Scan(&it.ID, &enc, &plain, &ct); err != nil {
				rows.Close()
				return nil, fmt.Errorf("cortex: scan pending embedding: %w", err)
			}
			if err := c.CheckRowForm(enc); err != nil {
				rows.Close()
				return nil, err
			}
			if enc {
				if it.Text, err = c.OpenStr(reembedTables[k.table], it.ID, k.col, ct); err != nil {
					rows.Close()
					return nil, err
				}
			} else {
				it.Text = plain.String
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
			if err := c.fillMemoryContextEnc(ctx, q, out[start:]); err != nil {
				return nil, err
			}
		}
		for i := start; i < len(out); i++ {
			out[i].Text = TruncateEmbedText(out[i].Text)
		}
	}
	return out, nil
}

func (c Ops) fillMemoryContextEnc(ctx context.Context, q rowsQuerier, items []model.EmbedItem) error {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	rows, err := q.QueryContext(ctx,
		`SELECT me.memory_id, e.id, e.enc, e.name, e.name_ct FROM memory_entities me
		 JOIN entities e ON e.id = me.entity_id AND e.user_id = me.user_id
		 WHERE me.user_id = $1 AND me.memory_id = ANY($2)`, c.UserID, ids)
	if err != nil {
		return fmt.Errorf("cortex: memory entity names: %w", err)
	}
	defer rows.Close()
	names := map[string][]string{}
	for rows.Next() {
		var memID, entID string
		var enc bool
		var plain sql.NullString
		var ct []byte
		if err := rows.Scan(&memID, &entID, &enc, &plain, &ct); err != nil {
			return fmt.Errorf("cortex: scan entity name: %w", err)
		}
		if err := c.CheckRowForm(enc); err != nil {
			return err
		}
		name := plain.String
		if enc {
			if name, err = c.OpenStr(store.TblEntities, entID, "name", ct); err != nil {
				return err
			}
		}
		names[memID] = append(names[memID], name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range items {
		items[i].Text = AugmentForEmbedding(items[i].Text, names[items[i].ID])
	}
	return nil
}
