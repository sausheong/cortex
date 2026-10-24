package cortex

import (
	"context"

	"github.com/sausheong/cortex/internal/ingest"
)

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
