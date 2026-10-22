package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"
)

const reembedBatch = 64

// errTailTooLarge makes the flip roll back so the loop can drain more rows.
var errTailTooLarge = errors.New("jobs: re-embed tail too large")

func embedItems(ctx context.Context, target cortex.Embedder, items []cortex.EmbedItem) ([][]float32, error) {
	texts := make([]string, len(items))
	for i, it := range items {
		texts[i] = it.Text
	}
	vecs, err := target.Embed(ctx, texts)
	if err != nil {
		return nil, err
	}
	if len(vecs) != len(items) {
		return nil, fmt.Errorf("jobs: embedder returned %d vectors for %d texts", len(vecs), len(items))
	}
	return vecs, nil
}

// ReEmbed writes generation active+1 for every entity, memory and chunk of
// cx's user (soft-deleted rows included) with target, then flips the active
// generation under the per-user lock. It is resumable: rows that already
// have the new generation are skipped. On any failure the old generation
// stays active.
func ReEmbed(ctx context.Context, cx *cortex.Cortex, st *accounts.Store, target cortex.Embedder, report func(done, total int)) error {
	return ReEmbedWith(ctx, cx, st, target, nil, report)
}

// ReEmbedWith is ReEmbed, but when set is non-nil the flip promotes exactly
// those settings (the ones target was built from) instead of re-reading the
// user's current desired settings, which may have changed during the job.
func ReEmbedWith(ctx context.Context, cx *cortex.Cortex, st *accounts.Store, target cortex.Embedder, set *accounts.Settings, report func(done, total int)) error {
	if report == nil {
		report = func(int, int) {}
	}
	if err := EnsureIndexes(ctx, cx, target.Dimensions()); err != nil {
		return err
	}
	active, err := cx.ActiveGeneration(ctx)
	if err != nil {
		return err
	}
	newGen := active + 1
	total, err := cx.CountEmbeddable(ctx)
	if err != nil {
		return err
	}
	progress := func() (int, error) {
		n, err := cx.CountPendingEmbeddings(ctx, newGen)
		if err != nil {
			return 0, err
		}
		if d := total - n; d >= 0 {
			report(d, total)
		} else {
			report(total, total)
		}
		return n, nil
	}

	for {
		// Drain outside the lock until only a short tail remains.
		for {
			n, err := progress()
			if err != nil {
				return err
			}
			if n <= reembedBatch {
				break
			}
			items, err := cx.PendingEmbeddings(ctx, newGen, reembedBatch)
			if err != nil {
				return err
			}
			vecs, err := embedItems(ctx, target, items)
			if err != nil {
				return err
			}
			for i, it := range items {
				if err := cx.PutEmbeddingGen(ctx, it.ID, it.RefType, newGen, vecs[i]); err != nil {
					return err
				}
			}
		}

		embedTail := func(ctx context.Context, tx *sql.Tx, put func(refID, refType string, v []float32) error) error {
			items, err := cx.PendingEmbeddingsTx(ctx, tx, newGen, reembedBatch+1)
			if err != nil {
				return err
			}
			if len(items) > reembedBatch {
				return errTailTooLarge
			}
			if len(items) == 0 {
				return nil
			}
			vecs, err := embedItems(ctx, target, items)
			if err != nil {
				return err
			}
			for i, it := range items {
				if err := put(it.ID, it.RefType, vecs[i]); err != nil {
					return err
				}
			}
			return nil
		}
		inTx := func(tx *sql.Tx) error {
			if set != nil {
				return st.PromoteActiveEmbedSettingsTx(ctx, tx, cx.UserID(), *set)
			}
			return st.PromoteActiveEmbedTx(ctx, tx, cx.UserID())
		}
		err := cx.FlipGeneration(ctx, newGen, embedTail, inTx)
		if errors.Is(err, errTailTooLarge) {
			continue
		}
		if err != nil {
			return err
		}
		report(total, total)
		return nil
	}
}
