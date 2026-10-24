package cortex

import (
	"context"
	"database/sql"
	"time"

	"github.com/sausheong/cortex/internal/memory"
)

// PutMemory inserts a memory and its entity links into the database.
// The operation is wrapped in a transaction. The memory's ID, CreatedAt,
// and UpdatedAt are set on the passed struct.
func (c *Cortex) PutMemory(ctx context.Context, m *Memory) error {
	return memory.New(c.h).PutMemory(ctx, m)
}

// SearchMemories performs an FTS5 full-text search on memory content,
// ranked by relevance (best first). The query is sanitized into a safe
// FTS5 MATCH expression. Entity links are loaded from memory_entities for
// each result.
func (c *Cortex) SearchMemories(ctx context.Context, query string, limit int) ([]Memory, error) {
	return memory.New(c.h).SearchMemories(ctx, query, limit)
}

// GetMemoriesByEntity returns all memories linked to the given entity.
// Entity links are loaded from memory_entities for each result.
func (c *Cortex) GetMemoriesByEntity(ctx context.Context, entityID string) ([]Memory, error) {
	return memory.New(c.h).GetMemoriesByEntity(ctx, entityID)
}

// InvalidateMemory soft-retires a memory: it sets expired_at to now (the
// system has retired this memory) and, when invalidAt is non-nil, sets
// invalid_at to the event time the fact stopped being true. The row, its
// embedding are left intact so the memory remains
// available to point-in-time history queries; default recall hides it via
// the currently-valid predicate. Returns an error if no memory has the
// given id. This is the primitive Phase 2b's reconciliation calls on
// supersession; it never deletes (use Forget for hard deletion).
func (c *Cortex) InvalidateMemory(ctx context.Context, id string, invalidAt *time.Time) error {
	return memory.New(c.h).InvalidateMemory(ctx, id, invalidAt)
}

// PutMemoryEdge inserts a typed memory-to-memory edge. It is idempotent on
// (source_id, target_id, type): re-inserting the same edge is a no-op (no
// error, no duplicate). The passed struct's ID (generated if empty) and
// CreatedAt are set; on a conflict no-op the row already in the table is kept
// and its id may differ from e.ID.
func (c *Cortex) PutMemoryEdge(ctx context.Context, e *MemoryEdge) error {
	return memory.New(c.h).PutMemoryEdge(ctx, e)
}

// GetMemoryEdges returns every edge where memoryID is the source or the
// target — the memory's full edge neighborhood.
func (c *Cortex) GetMemoryEdges(ctx context.Context, memoryID string) ([]MemoryEdge, error) {
	return memory.New(c.h).GetMemoryEdges(ctx, memoryID)
}

// GetMemoryEdgesByType returns edges of the given type where memoryID is the
// source or the target.
func (c *Cortex) GetMemoryEdgesByType(ctx context.Context, memoryID, edgeType string) ([]MemoryEdge, error) {
	return memory.New(c.h).GetMemoryEdgesByType(ctx, memoryID, edgeType)
}

// PutChunk inserts a chunk and its content (its tsv column is generated).
// The operation is wrapped in a transaction. The chunk's ID and CreatedAt
// are set on the passed struct.
func (c *Cortex) PutChunk(ctx context.Context, ch *Chunk) error {
	return memory.New(c.h).PutChunk(ctx, ch)
}

// SearchKeyword performs a full-text search on chunk content using Postgres tsvector.
// Results are ordered by relevance (rank). Returns up to limit results.
func (c *Cortex) SearchKeyword(ctx context.Context, query string, limit int) ([]Chunk, error) {
	return memory.New(c.h).SearchKeyword(ctx, query, limit)
}

// SearchVector embeds the query text using the configured embedder, then
// performs a brute-force vector search over chunk embeddings and returns
// the matching chunks ordered by similarity.
func (c *Cortex) SearchVector(ctx context.Context, query string, limit int) ([]Chunk, error) {
	return memory.New(c.h).SearchVector(ctx, query, limit)
}

// SearchMemoryVector embeds the query and performs a brute-force vector
// search over memory embeddings (ref_type='memory'), returning the matching
// memories ordered by cosine similarity. These embeddings are written at
// ingest by Remember but were previously never searched.
func (c *Cortex) SearchMemoryVector(ctx context.Context, query string, limit int) ([]Memory, error) {
	return memory.New(c.h).SearchMemoryVector(ctx, query, limit)
}

// ActiveGeneration returns the user's active embedding generation.
func (c *Cortex) ActiveGeneration(ctx context.Context) (int, error) {
	return memory.New(c.h).ActiveGeneration(ctx)
}

// SetActiveGeneration sets the user's active embedding generation.
func (c *Cortex) SetActiveGeneration(ctx context.Context, g int) error {
	return memory.New(c.h).SetActiveGeneration(ctx, g)
}

// PutEmbeddingGen is the re-embed writer. It accepts only gen equal to the
// active generation or active+1, and never applies the stale-pin check.
func (c *Cortex) PutEmbeddingGen(ctx context.Context, refID, refType string, gen int, v []float32) error {
	return memory.New(c.h).PutEmbeddingGen(ctx, refID, refType, gen, v)
}

// FlipGeneration switches the user to newGen in one transaction under the
// exclusive re-embed lock. embedTail embeds rows still lacking newGen using
// the supplied put (same tx, no second lock); other generations' rows are
// then deleted and inTx runs before commit. Any error rolls everything back.
func (c *Cortex) FlipGeneration(ctx context.Context, newGen int, embedTail func(ctx context.Context, tx *sql.Tx, put func(refID, refType string, v []float32) error) error, inTx func(tx *sql.Tx) error) error {
	return memory.New(c.h).FlipGeneration(ctx, newGen, embedTail, inTx)
}

// PendingEmbeddings returns up to limit rows (soft-deleted ones included)
// of this handle's user that have no embedding at generation gen, with the
// text to embed for each.
func (c *Cortex) PendingEmbeddings(ctx context.Context, gen, limit int) ([]EmbedItem, error) {
	return memory.New(c.h).PendingEmbeddings(ctx, gen, limit)
}

// PendingEmbeddingsTx is PendingEmbeddings inside an open transaction.
func (c *Cortex) PendingEmbeddingsTx(ctx context.Context, tx *sql.Tx, gen, limit int) ([]EmbedItem, error) {
	return memory.New(c.h).PendingEmbeddingsTx(ctx, tx, gen, limit)
}

// CountPendingEmbeddings counts rows (soft-deleted included) lacking an
// embedding at generation gen.
func (c *Cortex) CountPendingEmbeddings(ctx context.Context, gen int) (int, error) {
	return memory.New(c.h).CountPendingEmbeddings(ctx, gen)
}

// CountEmbeddable counts all rows that carry an embedding.
func (c *Cortex) CountEmbeddable(ctx context.Context) (int, error) {
	return memory.New(c.h).CountEmbeddable(ctx)
}

// MemoriesByEntityAll returns every currently-valid memory grouped by the
// entity it is linked to, in one query. It is the bulk form of
// GetMemoriesByEntity for whole-graph builds (the vault), which would
// otherwise make one round trip per entity plus one per memory. Memories are
// ordered oldest first; EntityIDs is filled from the same links.
func (c *Cortex) MemoriesByEntityAll(ctx context.Context) (map[string][]Memory, error) {
	return memory.New(c.h).MemoriesByEntityAll(ctx)
}

// AllRelationships returns every live relationship for the user in one
// query, ordered by id. It is the bulk form of GetRelationships.
func (c *Cortex) AllRelationships(ctx context.Context) ([]Relationship, error) {
	return memory.New(c.h).AllRelationships(ctx)
}

// WithGeneration returns a handle pinned to embedding generation g for
// ordinary writers. Its embedding writes fail with ErrStaleGeneration once
// the active generation differs from g.
func (c *Cortex) WithGeneration(g int) *Cortex {
	h2 := *c.h
	h2.PinnedGen = g
	return &Cortex{h: &h2}
}
