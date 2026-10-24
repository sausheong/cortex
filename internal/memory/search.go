package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
)

// VectorResult holds a reference ID and its cosine similarity score.
type VectorResult struct {
	RefID      string
	Similarity float32
}

// EmbeddingByRef loads the stored embedding vector for a ref (chunk/memory),
// or nil if none exists. Lets the reranker reuse vectors written at ingest
// instead of re-embedding ranked results.
func (c Ops) EmbeddingByRef(ctx context.Context, refID, refType string) []float32 {
	var lit string
	err := c.DB.QueryRowContext(ctx,
		`SELECT embedding::text FROM embeddings WHERE user_id = ? AND ref_id = ? AND ref_type = ?
		 AND generation = (SELECT active_embedding_generation FROM users WHERE id = ?) LIMIT 1`,
		c.UserID, refID, refType, c.UserID,
	).Scan(&lit)
	if err != nil {
		return nil
	}
	v, err := ParseVectorLiteral(lit)
	if err != nil {
		return nil
	}
	return v
}

// SearchVectorRaw performs cosine nearest-neighbour search over embeddings of
// the given refType using the partial HNSW index, returning the top limit
// results with similarity = 1 - cosine distance.
func (c Ops) SearchVectorRaw(ctx context.Context, queryVec []float32, refType string, limit int) ([]VectorResult, error) {
	dims := len(queryVec)
	if dims < 1 || dims > 4000 {
		return nil, fmt.Errorf("cortex: query vector has %d dims, want 1..4000", dims)
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("cortex: begin vector search: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SET LOCAL hnsw.iterative_scan = strict_order`); err != nil {
		return nil, fmt.Errorf("cortex: set iterative_scan: %w", err)
	}
	lit := VectorLiteral(queryVec)
	// Soft-deleted rows keep their embeddings (restore needs them), so the live
	// check must be part of the query: otherwise they crowd out live hits in the
	// top-K. Only memory and chunk refs are soft-deletable.
	live := ""
	switch refType {
	case "memory":
		live = ` AND EXISTS (SELECT 1 FROM memories m WHERE m.id = embeddings.ref_id AND m.user_id = embeddings.user_id AND m.deleted_at IS NULL)`
	case "chunk":
		live = ` AND EXISTS (SELECT 1 FROM chunks k WHERE k.id = embeddings.ref_id AND k.user_id = embeddings.user_id AND k.deleted_at IS NULL)`
	}
	rows, err := tx.QueryContext(ctx,
		fmt.Sprintf(`SELECT ref_id, 1 - (embedding::halfvec(%d) <=> ?::halfvec) AS sim
		 FROM embeddings WHERE user_id = ? AND ref_type = ? AND dims = %d
		 AND generation = (SELECT active_embedding_generation FROM users WHERE id = ?)%s
		 ORDER BY embedding::halfvec(%d) <=> ?::halfvec LIMIT ?`, dims, dims, live, dims),
		lit, c.UserID, refType, c.UserID, lit, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("cortex: query embeddings: %w", err)
	}
	defer rows.Close()

	var results []VectorResult
	for rows.Next() {
		var r VectorResult
		var sim float64
		if err := rows.Scan(&r.RefID, &sim); err != nil {
			return nil, fmt.Errorf("cortex: scan embedding: %w", err)
		}
		r.Similarity = float32(sim)
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cortex: iterate embeddings: %w", err)
	}
	return results, nil
}

// SearchVector embeds the query text using the configured embedder, then
// performs a brute-force vector search over chunk embeddings and returns
// the matching chunks ordered by similarity.
func (c Ops) SearchVector(ctx context.Context, query string, limit int) ([]model.Chunk, error) {
	if c.Cfg.Embedder == nil {
		return nil, fmt.Errorf("cortex: no embedder configured")
	}

	vecs, err := c.Cfg.Embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("cortex: embed query: %w", err)
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("cortex: embedder returned no vectors")
	}

	results, err := c.SearchVectorRaw(ctx, vecs[0], "chunk", limit)
	if err != nil {
		return nil, err
	}

	// Look up each chunk by ID.
	chunks := make([]model.Chunk, 0, len(results))
	for _, r := range results {
		var ch model.Chunk
		var entityID sql.NullString
		var metaJSON sql.NullString
		err := c.DB.QueryRowContext(ctx,
			`SELECT id, entity_id, content, metadata, created_at
			 FROM chunks WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, r.RefID, c.UserID,
		).Scan(&ch.ID, &entityID, &ch.Content, &metaJSON, &ch.CreatedAt)
		if err == sql.ErrNoRows {
			continue // embedding exists but chunk was deleted
		}
		if err != nil {
			return nil, fmt.Errorf("cortex: get chunk %s: %w", r.RefID, err)
		}
		if entityID.Valid {
			ch.EntityID = entityID.String
		}
		if metaJSON.Valid && metaJSON.String != "" {
			if err := json.Unmarshal([]byte(metaJSON.String), &ch.Metadata); err != nil {
				return nil, fmt.Errorf("cortex: unmarshal chunk metadata: %w", err)
			}
		}
		chunks = append(chunks, ch)
	}
	return chunks, nil
}

// SearchMemoryVector embeds the query and performs a brute-force vector
// search over memory embeddings (ref_type='memory'), returning the matching
// memories ordered by cosine similarity. These embeddings are written at
// ingest by Remember but were previously never searched.
func (c Ops) SearchMemoryVector(ctx context.Context, query string, limit int) ([]model.Memory, error) {
	return c.searchMemoryVectorMode(ctx, query, limit, model.TemporalMode{})
}

// searchMemoryVectorMode is the temporal-mode-aware backing for
// SearchMemoryVector. The per-row resolve query's validity predicate is
// derived from mode (default = currently-valid).
func (c Ops) searchMemoryVectorMode(ctx context.Context, query string, limit int, mode model.TemporalMode) ([]model.Memory, error) {
	if c.Cfg.Embedder == nil {
		return nil, fmt.Errorf("cortex: no embedder configured")
	}

	vecs, err := c.Cfg.Embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("cortex: embed query: %w", err)
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("cortex: embedder returned no vectors")
	}

	results, err := c.SearchVectorRaw(ctx, vecs[0], "memory", limit)
	if err != nil {
		return nil, err
	}

	clause, targs := mode.Clause("")

	memories := make([]model.Memory, 0, len(results))
	for _, r := range results {
		var m model.Memory
		var spk sql.NullString
		var vat, iat, eat sql.NullTime
		// Arg order: ref id, user id, then temporal args.
		args := []any{r.RefID, c.UserID}
		args = append(args, targs...)
		err := c.DB.QueryRowContext(ctx,
			`SELECT id, content, source, speaker, confidence, created_at, updated_at, valid_at, invalid_at, expired_at
			 FROM memories WHERE id = ? AND user_id = ? AND deleted_at IS NULL AND `+clause, args...,
		).Scan(&m.ID, &m.Content, &m.Source, &spk, &m.Confidence, &m.CreatedAt, &m.UpdatedAt, &vat, &iat, &eat)
		if err == sql.ErrNoRows {
			continue // embedding exists but memory was deleted
		}
		if err != nil {
			return nil, fmt.Errorf("cortex: get memory %s: %w", r.RefID, err)
		}
		if spk.Valid {
			m.Speaker = spk.String
		}
		m.ValidAt = store.NullTimePtr(vat)
		m.InvalidAt = store.NullTimePtr(iat)
		m.ExpiredAt = store.NullTimePtr(eat)
		entityIDs, err := c.loadMemoryEntityIDs(ctx, m.ID)
		if err != nil {
			return nil, err
		}
		m.EntityIDs = entityIDs
		memories = append(memories, m)
	}
	return memories, nil
}
