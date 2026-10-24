package cortex

import (
	"context"
	"time"

	"github.com/sausheong/cortex/internal/graph"
)

// CountMemories returns how many live memories SoftDeleteMemories would mark
// for f, so a caller can show the exact number before deleting.
func (c *Cortex) CountMemories(ctx context.Context, f DeleteFilter) (int, error) {
	return graph.New(c.h).CountMemories(ctx, f)
}

// SampleMemories returns up to limit of the live memories SoftDeleteMemories
// would mark for f, newest first, so a caller can show what is about to go.
func (c *Cortex) SampleMemories(ctx context.Context, f DeleteFilter, limit int) ([]Memory, error) {
	return graph.New(c.h).SampleMemories(ctx, f, limit)
}

// CountByConnection returns how many rows SoftDeleteByConnection would mark
// for connID (memories, entities, chunks and relationships, including
// relationships that touch the connection's entities).
func (c *Cortex) CountByConnection(ctx context.Context, connID string) (int, error) {
	return graph.New(c.h).CountByConnection(ctx, connID)
}

// SoftDeleteMemories marks the memories matching f as deleted in one batch.
// The batch ID is empty when nothing matched.
func (c *Cortex) SoftDeleteMemories(ctx context.Context, f DeleteFilter, by string) (string, int, error) {
	return graph.New(c.h).SoftDeleteMemories(ctx, f, by)
}

// SoftDeleteEntity soft-deletes an entity, its relationships and its chunks.
func (c *Cortex) SoftDeleteEntity(ctx context.Context, id string, by string) (string, error) {
	return graph.New(c.h).SoftDeleteEntity(ctx, id, by)
}

// SoftDeleteByConnection soft-deletes everything the connection wrote:
// memories, entities, relationships and chunks, plus relationships that touch
// the removed entities. n counts all rows marked.
func (c *Cortex) SoftDeleteByConnection(ctx context.Context, connID string) (string, int, error) {
	return graph.New(c.h).SoftDeleteByConnection(ctx, connID)
}

// RestoreBatch reverses a batch of this user: undoes recorded merge changes
// (newest first), then un-deletes the batch's rows. An entity whose (name,
// type) is now taken by a live entity is merged into it instead.
func (c *Cortex) RestoreBatch(ctx context.Context, batchID string) error {
	return graph.New(c.h).RestoreBatch(ctx, batchID)
}

// ListDeleted returns this user's soft-deleted memories, newest deletion first.
func (c *Cortex) ListDeleted(ctx context.Context, limit int, offset int) ([]Memory, error) {
	return graph.New(c.h).ListDeleted(ctx, limit, offset)
}

// ListDeleteBatches lists this user's unrestored batches, newest first.
func (c *Cortex) ListDeleteBatches(ctx context.Context) ([]DeleteBatch, error) {
	return graph.New(c.h).ListDeleteBatches(ctx)
}

// PurgeDeleted hard-deletes this user's rows soft-deleted more than olderThan
// ago, with their embeddings (every generation), memory links and memory
// edges, in one transaction. Batches older than the cutoff, or already
// restored, are removed with their change records. It returns the number of
// memories, entities, relationships and chunks removed.
func (c *Cortex) PurgeDeleted(ctx context.Context, olderThan time.Duration) (int, error) {
	return graph.New(c.h).PurgeDeleted(ctx, olderThan)
}

// SoftDeleteChunks marks the chunks with the given IDs as deleted in one
// batch. The batch ID is empty when nothing matched.
func (c *Cortex) SoftDeleteChunks(ctx context.Context, ids []string, by string) (string, int, error) {
	return graph.New(c.h).SoftDeleteChunks(ctx, ids, by)
}
