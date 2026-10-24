package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/model"
)

// CompactionRow is a compactions row with its proposal decoded.
type CompactionRow struct {
	Status  string
	BatchID string
	Changes []model.Change
}

// StoreCompactionProposal records the proposal of compaction id and moves it
// from 'proposing' to 'ready'. The proposal (result and source texts) is
// sealed under uk (AAD column proposal_json); without a key it fails with
// model.ErrLocked (model.ErrEnrollRequired for an unenrolled user) and
// nothing is written.
func (st *Store) StoreCompactionProposal(ctx context.Context, userID, id string, uk *keys.UK, changes []model.Change) error {
	if changes == nil {
		changes = []model.Change{}
	}
	b, err := json.Marshal(changes)
	if err != nil {
		return err
	}
	if uk == nil {
		state, serr := st.encState(ctx, userID)
		if serr != nil {
			return serr
		}
		if state == "none" {
			return model.ErrEnrollRequired
		}
		return model.ErrLocked
	}
	ct, err := uk.Seal(userID, "compactions", id, "proposal_json", b)
	if err != nil {
		return err
	}
	if _, err = st.DB.ExecContext(ctx,
		`UPDATE compactions SET status = 'ready', proposal_ct = $3
		 WHERE id = $1 AND user_id = $2 AND status = 'proposing'`, id, userID, ct); err != nil {
		return fmt.Errorf("accounts: store compaction proposal: %w", err)
	}
	return nil
}

// LoadCompaction returns the user's own compaction; a foreign or absent id is
// sql.ErrNoRows. A stored proposal needs uk, else model.ErrLocked.
func (st *Store) LoadCompaction(ctx context.Context, userID, id string, uk *keys.UK) (*CompactionRow, error) {
	var row CompactionRow
	var batch sql.NullString
	var ct []byte
	err := st.DB.QueryRowContext(ctx,
		`SELECT status, proposal_ct, batch_id FROM compactions WHERE id = $1 AND user_id = $2`,
		id, userID).Scan(&row.Status, &ct, &batch)
	if err != nil {
		return nil, err
	}
	row.BatchID = batch.String
	if len(ct) > 0 {
		if uk == nil {
			return nil, model.ErrLocked
		}
		plain, err := uk.Open(userID, "compactions", id, "proposal_json", ct)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(plain, &row.Changes); err != nil {
			return nil, err
		}
	}
	return &row, nil
}
