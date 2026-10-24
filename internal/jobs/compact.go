package jobs

import (
	"context"
	"fmt"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/keys"
)

// CompactKindPrefix prefixes the kind of a compaction job: "compact:<id>".
const CompactKindPrefix = "compact:"

// CompactJob proposes a compaction for userID and stores it in the
// compactions row id (status proposing -> ready, or failed). The row must
// belong to userID.
func CompactJob(ctx context.Context, root *cortex.Cortex, st *accounts.Store, userID, id string) error {
	return CompactJobKey(ctx, root, st, userID, id, nil)
}

// CompactJobKey is CompactJob for a job that holds the user's key (nil for an
// unencrypted user). An encrypted user's proposal is sealed under uk.
func CompactJobKey(ctx context.Context, root *cortex.Cortex, st *accounts.Store, userID, id string, uk *keys.UK) (err error) {
	defer func() {
		if err != nil {
			// A cancelled context must still be able to record the failure.
			_, _ = st.DB.ExecContext(context.WithoutCancel(ctx),
				`UPDATE compactions SET status = 'failed' WHERE id = $1 AND user_id = $2 AND status = 'proposing'`, id, userID)
		}
	}()
	var n int
	if err := st.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM compactions WHERE id = $1 AND user_id = $2 AND status = 'proposing'`, id, userID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	cx, err := st.ResolveUser(ctx, root, userID, "", uk)
	if err != nil {
		return err
	}
	changes, err := cx.ProposeCompaction(ctx)
	if err != nil {
		return err
	}
	if err := st.StoreCompactionProposal(ctx, userID, id, uk, changes); err != nil {
		return fmt.Errorf("jobs: store compaction proposal: %w", err)
	}
	return nil
}
