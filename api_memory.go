package cortex

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sausheong/cortex/internal/ingest"
	"github.com/sausheong/cortex/internal/memory"
	"github.com/sausheong/cortex/internal/query"
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

// Remember extracts entities, relationships, memories, and chunks from the
// provided content and stores them in the knowledge graph.
func (c *Cortex) Remember(ctx context.Context, content string, opts ...RememberOption) error {
	return ingest.New(c.h).Remember(ctx, content, opts...)
}

// Forget removes knowledge from the graph based on the provided filter
// (EntityID or Source). The removal is a recoverable soft delete: matching
// entities, their relationships and chunks are marked deleted in one batch
// (see ListDeleteBatches, RestoreBatch) and purged after 30 days. Memory
// links are kept, so a memory whose only entities were forgotten is still
// recalled.
func (c *Cortex) Forget(ctx context.Context, f Filter) error {
	return ingest.New(c.h).Forget(ctx, f)
}

// RecallWithStrength runs Recall and adds an aggregate strength score plus
// an abstention hint. Use this when the caller (e.g. an agent) needs to
// decide whether the knowledge graph actually knows the answer.
func (c *Cortex) RecallWithStrength(ctx context.Context, query string, opts ...RecallOption) (RecallResult, error) {
	return memory.New(c.h).RecallWithStrength(ctx, query, opts...)
}

// Recall searches the knowledge graph using multiple retrieval strategies,
// merges the results via reciprocal rank fusion, and returns a unified
// ranked list of Result items.
func (c *Cortex) Recall(ctx context.Context, query string, opts ...RecallOption) ([]Result, error) {
	return memory.New(c.h).Recall(ctx, query, opts...)
}

// IsEmbedPartial reports whether err only means some embeddings are pending.
func IsEmbedPartial(err error) bool {
	var p *EmbedPartialError
	return errors.As(err, &p)
}

// IsExtractPartial reports whether err only means some sections were not
// analysed.
func IsExtractPartial(err error) bool {
	var p *ExtractPartialError
	return errors.As(err, &p)
}

// BackfillEmbeddings embeds up to limit rows that have no vector at the
// active generation (for example after a provider outage during Remember).
// It returns how many rows it embedded; a provider error stops the batch.
func (c *Cortex) BackfillEmbeddings(ctx context.Context, limit int) (int, error) {
	if c.h.Cfg.Embedder == nil {
		return 0, nil
	}
	gen, err := c.ActiveGeneration(ctx)
	if err != nil {
		return 0, err
	}
	items, err := c.PendingEmbeddings(ctx, gen, limit)
	if err != nil {
		return 0, err
	}
	done := 0
	for start := 0; start < len(items); start += memory.EmbedBatchSize {
		batch := items[start:min(start+memory.EmbedBatchSize, len(items))]
		texts := make([]string, len(batch))
		for i, it := range batch {
			texts[i] = it.Text
		}
		vecs, err := c.h.Cfg.Embedder.Embed(ctx, texts)
		if err != nil {
			return done, fmt.Errorf("cortex: backfill embedding: %w", err)
		}
		for i := 0; i < len(vecs) && i < len(batch); i++ {
			if err := c.PutEmbeddingGen(ctx, batch[i].ID, batch[i].RefType, gen, vecs[i]); err != nil {
				return done, err
			}
			done++
		}
	}
	return done, nil
}

// WhoIs resolves a mention (a name, alias, email, phone or handle) and
// returns the entity with its aliases, current relationships (named) and
// current memories, static ones first. When the mention does not resolve
// confidently, the card is empty and Candidates lists the closest entities.
func (c *Cortex) WhoIs(ctx context.Context, mention string) (*EntityCard, error) {
	return query.New(c.h).WhoIs(ctx, mention)
}

// Describe returns the EntityCard of a known entity.
func (c *Cortex) Describe(ctx context.Context, entityID string) (*EntityCard, error) {
	return query.New(c.h).Describe(ctx, entityID)
}

// Path returns a shortest chain of current relationships linking two
// entities (in either direction), at most maxHops long (default 4, capped at
// 6), or nil when they are not connected within that distance. The owner is
// only used as an end, never as a stepping stone: everything connects
// through the owner, which says nothing.
func (c *Cortex) Path(ctx context.Context, fromID, toID string, maxHops int) ([]NamedEdge, error) {
	return query.New(c.h).Path(ctx, fromID, toID, maxHops)
}

// Timeline returns an entity's history, oldest first: its memories (dated by
// when they became true, else when they were recorded), and the start and
// end of its relationships. since, when non-zero, drops earlier events;
// limit (default 100) keeps the most recent. Retired memories are included,
// so the timeline shows what used to be true.
func (c *Cortex) Timeline(ctx context.Context, entityID string, since time.Time, limit int) ([]TimelineEvent, error) {
	return query.New(c.h).Timeline(ctx, entityID, since, limit)
}

// WhatChanged lists what was added to or retired from the graph since t:
// new entities, memories and relationships (by when they were recorded),
// memories that stopped being current, and relationships whose end falls
// after t (by when they ended, which for a back-dated end may be before it
// was recorded). Each list holds at most limit items (default 50), newest
// first.
func (c *Cortex) WhatChanged(ctx context.Context, since time.Time, limit int) (*Changes, error) {
	return query.New(c.h).WhatChanged(ctx, since, limit)
}

// Link records a relationship known for certain (from structured data such
// as an email header), without free-text extraction. Each end is an entity
// ID, or a name or identifier resolved like an ingested mention; one that
// resolves to nothing is created with the given type (default person), and
// an identifier used as its name is kept as an alias. The verb is mapped
// onto the curated vocabulary as in Remember.
func (c *Cortex) Link(ctx context.Context, r LinkRequest) (*NamedEdge, error) {
	return query.New(c.h).Link(ctx, r)
}
