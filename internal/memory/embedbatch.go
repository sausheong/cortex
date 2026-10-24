package memory

import (
	"context"
	"fmt"

	"github.com/sausheong/cortex/internal/model"
)

// EmbedBatchSize is how many texts go into one Embed call. It keeps a
// multi-megabyte document to a few dozen provider round trips instead of one
// per chunk, while staying well under per-request token limits.
const EmbedBatchSize = 64

// EmbedTarget is one row waiting for a vector.
type EmbedTarget struct {
	RefID, RefType, Text string
}

// EmbedAndStoreBatch embeds the targets in batches (each text capped to the
// embedder input limit) and stores the vectors. A failed batch counts all of
// its items as pending and the next batch is still attempted, so one bad
// batch never aborts the save.
func (c Ops) EmbedAndStoreBatch(ctx context.Context, targets []EmbedTarget) *model.EmbedFailures {
	var fails model.EmbedFailures
	for start := 0; start < len(targets); start += EmbedBatchSize {
		end := min(start+EmbedBatchSize, len(targets))
		batch := targets[start:end]
		texts := make([]string, len(batch))
		for i, t := range batch {
			texts[i] = TruncateEmbedText(t.Text)
		}
		vecs, err := c.Cfg.Embedder.Embed(ctx, texts)
		if err != nil {
			fails.AddN(len(batch), fmt.Errorf("embed %s: %w", batch[0].RefType, err))
			continue
		}
		if len(vecs) == 0 {
			continue
		}
		if len(vecs) < len(batch) {
			fails.AddN(len(batch)-len(vecs), fmt.Errorf("embed %s: got %d vectors for %d texts", batch[0].RefType, len(vecs), len(batch)))
		}
		for i := 0; i < len(vecs) && i < len(batch); i++ {
			if err := c.PutEmbedding(ctx, batch[i].RefID, batch[i].RefType, vecs[i]); err != nil {
				fails.Add(fmt.Errorf("store %s embedding: %w", batch[i].RefType, err))
			}
		}
	}
	return &fails
}
