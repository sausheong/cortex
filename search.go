package cortex

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// vectorResult holds a reference ID and its cosine similarity score.
type vectorResult struct {
	refID      string
	similarity float32
}

// embeddingByRef loads the stored embedding vector for a ref (chunk/memory),
// or nil if none exists. Lets the reranker reuse vectors written at ingest
// instead of re-embedding ranked results.
func (c *Cortex) embeddingByRef(ctx context.Context, refID, refType string) []float32 {
	var lit string
	err := c.db.QueryRowContext(ctx,
		`SELECT embedding::text FROM embeddings WHERE ref_id = ? AND ref_type = ? LIMIT 1`,
		refID, refType,
	).Scan(&lit)
	if err != nil {
		return nil
	}
	v, err := parseVectorLiteral(lit)
	if err != nil {
		return nil
	}
	return v
}

// putEmbedding stores an embedding vector (halfvec) for a given reference.
func (c *Cortex) putEmbedding(ctx context.Context, refID, refType string, embedding []float32) error {
	if len(embedding) != c.dims {
		return fmt.Errorf("cortex: embedding has %d dims, want %d", len(embedding), c.dims)
	}
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO embeddings (id, ref_id, ref_type, embedding, created_at)
		 VALUES (?, ?, ?, ?::halfvec, now())`,
		newID(), refID, refType, vectorLiteral(embedding),
	)
	if err != nil {
		return fmt.Errorf("cortex: insert embedding: %w", err)
	}
	return nil
}

// searchVectorRaw performs cosine nearest-neighbour search over embeddings of
// the given refType using the partial HNSW index, returning the top limit
// results with similarity = 1 - cosine distance.
func (c *Cortex) searchVectorRaw(ctx context.Context, queryVec []float32, refType string, limit int) ([]vectorResult, error) {
	if len(queryVec) != c.dims {
		return nil, fmt.Errorf("cortex: query vector has %d dims, want %d", len(queryVec), c.dims)
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("cortex: begin vector search: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SET LOCAL hnsw.iterative_scan = relaxed_order`); err != nil {
		return nil, fmt.Errorf("cortex: set iterative_scan: %w", err)
	}
	lit := vectorLiteral(queryVec)
	rows, err := tx.QueryContext(ctx,
		`SELECT ref_id, 1 - (embedding <=> ?::halfvec) AS sim
		 FROM embeddings WHERE ref_type = ?
		 ORDER BY embedding <=> ?::halfvec LIMIT ?`,
		lit, refType, lit, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("cortex: query embeddings: %w", err)
	}
	defer rows.Close()

	var results []vectorResult
	for rows.Next() {
		var r vectorResult
		var sim float64
		if err := rows.Scan(&r.refID, &sim); err != nil {
			return nil, fmt.Errorf("cortex: scan embedding: %w", err)
		}
		r.similarity = float32(sim)
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
func (c *Cortex) SearchVector(ctx context.Context, query string, limit int) ([]Chunk, error) {
	if c.cfg.embedder == nil {
		return nil, fmt.Errorf("cortex: no embedder configured")
	}

	vecs, err := c.cfg.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("cortex: embed query: %w", err)
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("cortex: embedder returned no vectors")
	}

	results, err := c.searchVectorRaw(ctx, vecs[0], "chunk", limit)
	if err != nil {
		return nil, err
	}

	// Look up each chunk by ID.
	chunks := make([]Chunk, 0, len(results))
	for _, r := range results {
		var ch Chunk
		var entityID sql.NullString
		var metaJSON sql.NullString
		err := c.db.QueryRowContext(ctx,
			`SELECT id, entity_id, content, metadata, created_at
			 FROM chunks WHERE id = ?`, r.refID,
		).Scan(&ch.ID, &entityID, &ch.Content, &metaJSON, &ch.CreatedAt)
		if err == sql.ErrNoRows {
			continue // embedding exists but chunk was deleted
		}
		if err != nil {
			return nil, fmt.Errorf("cortex: get chunk %s: %w", r.refID, err)
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
func (c *Cortex) SearchMemoryVector(ctx context.Context, query string, limit int) ([]Memory, error) {
	return c.searchMemoryVectorMode(ctx, query, limit, temporalMode{})
}

// searchMemoryVectorMode is the temporal-mode-aware backing for
// SearchMemoryVector. The per-row resolve query's validity predicate is
// derived from mode (default = currently-valid).
func (c *Cortex) searchMemoryVectorMode(ctx context.Context, query string, limit int, mode temporalMode) ([]Memory, error) {
	if c.cfg.embedder == nil {
		return nil, fmt.Errorf("cortex: no embedder configured")
	}

	vecs, err := c.cfg.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("cortex: embed query: %w", err)
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("cortex: embedder returned no vectors")
	}

	results, err := c.searchVectorRaw(ctx, vecs[0], "memory", limit)
	if err != nil {
		return nil, err
	}

	clause, targs := mode.clause("")

	memories := make([]Memory, 0, len(results))
	for _, r := range results {
		var m Memory
		var spk sql.NullString
		var vat, iat, eat sql.NullTime
		// Arg order: ref id first, then temporal args.
		args := []any{r.refID}
		args = append(args, targs...)
		err := c.db.QueryRowContext(ctx,
			`SELECT id, content, source, speaker, confidence, created_at, updated_at, valid_at, invalid_at, expired_at
			 FROM memories WHERE id = ? AND `+clause, args...,
		).Scan(&m.ID, &m.Content, &m.Source, &spk, &m.Confidence, &m.CreatedAt, &m.UpdatedAt, &vat, &iat, &eat)
		if err == sql.ErrNoRows {
			continue // embedding exists but memory was deleted
		}
		if err != nil {
			return nil, fmt.Errorf("cortex: get memory %s: %w", r.refID, err)
		}
		if spk.Valid {
			m.Speaker = spk.String
		}
		m.ValidAt = nullTimePtr(vat)
		m.InvalidAt = nullTimePtr(iat)
		m.ExpiredAt = nullTimePtr(eat)
		entityIDs, err := c.loadMemoryEntityIDs(ctx, m.ID)
		if err != nil {
			return nil, err
		}
		m.EntityIDs = entityIDs
		memories = append(memories, m)
	}
	return memories, nil
}
