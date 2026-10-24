package cortex

import (
	"context"
	"errors"
	"fmt"

	"github.com/sausheong/cortex/internal/model"
)

// IsEmbedPartial reports whether err only means some embeddings are pending.
func IsEmbedPartial(err error) bool {
	var p *EmbedPartialError
	return errors.As(err, &p)
}

// embedBatchSize is how many texts go into one Embed call. It keeps a
// multi-megabyte document to a few dozen provider round trips instead of one
// per chunk, while staying well under per-request token limits.
const embedBatchSize = 64

// embedTarget is one row waiting for a vector.
type embedTarget struct {
	refID, refType, text string
}

// embedAndStoreBatch embeds the targets in batches (each text capped to the
// embedder input limit) and stores the vectors. A failed batch counts all of
// its items as pending and the next batch is still attempted, so one bad
// batch never aborts the save.
func (c *Cortex) embedAndStoreBatch(ctx context.Context, targets []embedTarget) *model.EmbedFailures {
	var fails model.EmbedFailures
	for start := 0; start < len(targets); start += embedBatchSize {
		end := min(start+embedBatchSize, len(targets))
		batch := targets[start:end]
		texts := make([]string, len(batch))
		for i, t := range batch {
			texts[i] = truncateEmbedText(t.text)
		}
		vecs, err := c.h.Cfg.Embedder.Embed(ctx, texts)
		if err != nil {
			fails.AddN(len(batch), fmt.Errorf("embed %s: %w", batch[0].refType, err))
			continue
		}
		if len(vecs) == 0 {
			continue
		}
		if len(vecs) < len(batch) {
			fails.AddN(len(batch)-len(vecs), fmt.Errorf("embed %s: got %d vectors for %d texts", batch[0].refType, len(vecs), len(batch)))
		}
		for i := 0; i < len(vecs) && i < len(batch); i++ {
			if err := c.putEmbedding(ctx, batch[i].refID, batch[i].refType, vecs[i]); err != nil {
				fails.Add(fmt.Errorf("store %s embedding: %w", batch[i].refType, err))
			}
		}
	}
	return &fails
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
	for start := 0; start < len(items); start += embedBatchSize {
		batch := items[start:min(start+embedBatchSize, len(items))]
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
