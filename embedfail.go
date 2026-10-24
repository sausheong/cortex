package cortex

import (
	"context"
	"errors"
	"fmt"

	"github.com/sausheong/cortex/internal/memory"
)

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
