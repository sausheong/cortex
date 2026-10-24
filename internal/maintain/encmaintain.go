package maintain

import (
	"context"
	"fmt"

	"github.com/sausheong/cortex/internal/memory"
	"github.com/sausheong/cortex/internal/workset"
)

// Encrypted-user variants of the maintenance readers: ids, flags and times
// come from SQL (plain columns), text and vectors from the working set and
// the embedding reader.

func (c Ops) memoryContent(ctx context.Context, id string) (string, error) {
	ws, err := workset.For(ctx, c.Handle)
	if err != nil {
		return "", err
	}
	m, ok := ws.MemoryByID(id)
	if !ok {
		return "", nil
	}
	return m.Content, nil
}

// compactCandidatesEnc is compactCandidates for encrypted users.
func (c Ops) compactCandidatesEnc(ctx context.Context) ([]compactCand, error) {
	ws, err := workset.For(ctx, c.Handle)
	if err != nil {
		return nil, err
	}
	rows, err := c.DB.QueryContext(ctx,
		`SELECT m.id, m.static, m.created_at
		 FROM memories m JOIN embeddings e
		   ON e.ref_id = m.id AND e.ref_type = 'memory' AND e.user_id = m.user_id
		  AND e.generation = (SELECT active_embedding_generation FROM users WHERE id = ?)
		 WHERE m.user_id = ? AND m.deleted_at IS NULL
		 ORDER BY m.created_at DESC, m.id LIMIT ?`, c.UserID, c.UserID, compactMaxCands)
	if err != nil {
		return nil, fmt.Errorf("cortex: compaction candidates: %w", err)
	}
	var out []compactCand
	for rows.Next() {
		var k compactCand
		if err := rows.Scan(&k.id, &k.static, &k.created); err != nil {
			rows.Close()
			return nil, fmt.Errorf("cortex: scan compaction candidate: %w", err)
		}
		out = append(out, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if m, ok := ws.MemoryByID(out[i].id); ok {
			out[i].content = m.Content
		}
		v, err := memory.New(c.Handle).GetEmbedding(ctx, out[i].id, "memory")
		if err != nil {
			return nil, err
		}
		out[i].vec = v
	}
	return out, nil
}

// retireCompactionResultsEnc soft-deletes the memories a compaction batch
// wrote; their source is ciphertext, so they are found in the working set.
func (c Ops) retireCompactionResultsEnc(ctx context.Context, source string) error {
	ws, err := workset.For(ctx, c.Handle)
	if err != nil {
		return err
	}
	var ids []string
	for _, m := range ws.Memories() {
		if m.Source == source {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	if _, err := c.DB.ExecContext(ctx,
		`UPDATE memories SET deleted_at = now(), deleted_by = 'compaction-undo'
		 WHERE user_id = ? AND deleted_at IS NULL AND id = ANY(?)`, c.UserID, ids); err != nil {
		return fmt.Errorf("cortex: retire compaction results: %w", err)
	}
	return nil
}
