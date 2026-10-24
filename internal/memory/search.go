package memory

import (
	"context"
	"fmt"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/textsearch"
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
	v, err := c.GetEmbedding(ctx, refID, refType)
	if err != nil {
		return nil
	}
	return v
}

// SearchVectorRaw performs a cosine nearest-neighbour search over the
// decrypted embeddings of the given refType, returning the top limit results
// with similarity = cosine similarity.
func (c Ops) SearchVectorRaw(ctx context.Context, queryVec []float32, refType string, limit int) ([]VectorResult, error) {
	dims := len(queryVec)
	if dims < 1 || dims > 4000 {
		return nil, fmt.Errorf("cortex: query vector has %d dims, want 1..4000", dims)
	}
	return c.searchVectorRawEnc(ctx, queryVec, refType, limit)
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

	hits := make([]textsearch.Hit, len(results))
	for i, r := range results {
		hits[i] = textsearch.Hit{ID: r.RefID}
	}
	return c.chunksByHits(ctx, hits)
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

	return c.memoriesByVectorHits(ctx, results, mode)
}
