package cortex

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
)

// vectorResult holds a reference ID and its cosine similarity score.
type vectorResult struct {
	refID      string
	similarity float32
}

// putEmbedding stores an embedding vector for a given reference (e.g. a chunk)
// in the embeddings table as a BLOB.
func (c *Cortex) putEmbedding(ctx context.Context, refID, refType string, embedding []float32) error {
	id := newID()
	blob := encodeFloat32s(embedding)

	_, err := c.db.ExecContext(ctx,
		`INSERT INTO embeddings (id, ref_id, ref_type, vector, created_at)
		 VALUES (?, ?, ?, ?, datetime('now'))`,
		id, refID, refType, blob,
	)
	if err != nil {
		return fmt.Errorf("cortex: insert embedding: %w", err)
	}
	return nil
}

// searchVectorRaw performs brute-force cosine similarity search over all
// embeddings of the given refType. It scans every row, computes similarity
// against queryVec, sorts descending, and returns the top limit results.
func (c *Cortex) searchVectorRaw(ctx context.Context, queryVec []float32, refType string, limit int) ([]vectorResult, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT ref_id, vector FROM embeddings WHERE ref_type = ?`,
		refType,
	)
	if err != nil {
		return nil, fmt.Errorf("cortex: query embeddings: %w", err)
	}
	defer rows.Close()

	var results []vectorResult
	for rows.Next() {
		var refID string
		var blob []byte
		if err := rows.Scan(&refID, &blob); err != nil {
			return nil, fmt.Errorf("cortex: scan embedding: %w", err)
		}

		vec := decodeFloat32s(blob)
		sim := cosineSimilarity(queryVec, vec)
		results = append(results, vectorResult{refID: refID, similarity: sim})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cortex: iterate embeddings: %w", err)
	}

	// Sort by similarity descending.
	sort.Slice(results, func(i, j int) bool {
		return results[i].similarity > results[j].similarity
	})

	// Return top limit.
	if len(results) > limit {
		results = results[:limit]
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
