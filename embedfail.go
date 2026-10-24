package cortex

import (
	"context"
	"errors"
	"fmt"
)

// DefaultMaxChunkChars is the chunk size Remember uses unless told
// otherwise: about 1,500 tokens of English, well inside the input limit of
// common embedding models (OpenAI text-embedding-3-*, nomic, bge).
const DefaultMaxChunkChars = 6000

// EmbedPartialError reports that Remember stored everything but could not
// embed some items. Keyword search finds them now; nightly maintenance
// backfills the vectors so semantic search catches up.
type EmbedPartialError struct {
	Count int
	Err   error // the first embedding failure
}

func (e *EmbedPartialError) Error() string {
	return fmt.Sprintf("cortex: saved, but %d item(s) are waiting for an embedding (retried nightly): %v", e.Count, e.Err)
}

func (e *EmbedPartialError) Unwrap() error { return e.Err }

// IsEmbedPartial reports whether err only means some embeddings are pending.
func IsEmbedPartial(err error) bool {
	var p *EmbedPartialError
	return errors.As(err, &p)
}

type embedFailures struct {
	n     int
	first error
}

func (f *embedFailures) add(err error) {
	if err == nil {
		return
	}
	f.n++
	if f.first == nil {
		f.first = err
	}
}

func (f *embedFailures) err() error {
	if f.n == 0 {
		return nil
	}
	return &EmbedPartialError{Count: f.n, Err: f.first}
}

// embedAndStore embeds text (capped to the embedder input limit) and stores
// the vector for ref. It returns the failure instead of aborting the caller.
func (c *Cortex) embedAndStore(ctx context.Context, refID, refType, text string) error {
	vecs, err := c.cfg.embedder.Embed(ctx, []string{truncateEmbedText(text)})
	if err != nil {
		return fmt.Errorf("embed %s: %w", refType, err)
	}
	if len(vecs) == 0 {
		return nil
	}
	if err := c.putEmbedding(ctx, refID, refType, vecs[0]); err != nil {
		return fmt.Errorf("store %s embedding: %w", refType, err)
	}
	return nil
}

// BackfillEmbeddings embeds up to limit rows that have no vector at the
// active generation (for example after a provider outage during Remember).
// It returns how many rows it embedded; a provider error stops the batch.
func (c *Cortex) BackfillEmbeddings(ctx context.Context, limit int) (int, error) {
	if c.cfg.embedder == nil {
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
	for _, it := range items {
		vecs, err := c.cfg.embedder.Embed(ctx, []string{it.Text})
		if err != nil {
			return done, fmt.Errorf("cortex: backfill embedding: %w", err)
		}
		if len(vecs) == 0 {
			continue
		}
		if err := c.PutEmbeddingGen(ctx, it.ID, it.RefType, gen, vecs[0]); err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}
