// Package jobs runs per-user background work: index builds and re-embeds.
package jobs

import (
	"context"
	"fmt"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/keys"
)

// EnsureIndexes creates the partial HNSW indexes for dims on entity, memory
// and chunk embeddings.
func EnsureIndexes(ctx context.Context, cx *cortex.Cortex, dims int) error {
	for _, rt := range []string{"entity", "memory", "chunk"} {
		if err := cx.EnsureVectorIndex(ctx, dims, rt); err != nil {
			return err
		}
	}
	return nil
}

// IndexJob is the body of an "index" job: it builds the indexes for the
// user's active embedding dimensions.
func IndexJob(ctx context.Context, root *cortex.Cortex, st *accounts.Store, userID string) error {
	return IndexJobKey(ctx, root, st, userID, nil)
}

// IndexJobKey is IndexJob for a job holding the user's key: an encrypted
// user's active embedder lives in the sealed settings blob, so the job is
// parked by the pool until a key is attached and then runs with it.
func IndexJobKey(ctx context.Context, root *cortex.Cortex, st *accounts.Store, userID string, uk *keys.UK) error {
	active, err := st.GetActiveEmbed(ctx, userID, uk)
	if err != nil {
		return fmt.Errorf("jobs: active embed: %w", err)
	}
	return EnsureIndexes(ctx, root.ForUser(userID), active.EmbedDims)
}
