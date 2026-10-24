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
// from 'proposing' to 'ready'. For an encrypted user the proposal (result and
// source texts) is sealed under uk (AAD column proposal_json); without a key
// it fails with model.ErrLocked and nothing is written.
func (st *Store) StoreCompactionProposal(ctx context.Context, userID, id string, uk *keys.UK, changes []model.Change) error {
	if changes == nil {
		changes = []model.Change{}
	}
	b, err := json.Marshal(changes)
	if err != nil {
		return err
	}
	state, err := st.encState(ctx, userID)
	if err != nil {
		return err
	}
	if state == "none" {
		_, err = st.DB.ExecContext(ctx,
			`UPDATE compactions SET status = 'ready', proposal_json = $3::jsonb, proposal_ct = NULL, enc = FALSE
			 WHERE id = $1 AND user_id = $2 AND status = 'proposing'`, id, userID, string(b))
	} else {
		if uk == nil {
			return model.ErrLocked
		}
		ct, serr := uk.Seal(userID, "compactions", id, "proposal_json", b)
		if serr != nil {
			return serr
		}
		_, err = st.DB.ExecContext(ctx,
			`UPDATE compactions SET status = 'ready', proposal_json = NULL, proposal_ct = $3, enc = TRUE
			 WHERE id = $1 AND user_id = $2 AND status = 'proposing'`, id, userID, ct)
	}
	if err != nil {
		return fmt.Errorf("accounts: store compaction proposal: %w", err)
	}
	return nil
}

// LoadCompaction returns the user's own compaction; a foreign or absent id is
// sql.ErrNoRows. A sealed proposal needs uk, else model.ErrLocked.
func (st *Store) LoadCompaction(ctx context.Context, userID, id string, uk *keys.UK) (*CompactionRow, error) {
	var row CompactionRow
	var proposal, batch sql.NullString
	var ct []byte
	var enc bool
	err := st.DB.QueryRowContext(ctx,
		`SELECT status, proposal_json::text, proposal_ct, enc, batch_id FROM compactions WHERE id = $1 AND user_id = $2`,
		id, userID).Scan(&row.Status, &proposal, &ct, &enc, &batch)
	if err != nil {
		return nil, err
	}
	row.BatchID = batch.String
	var plain []byte
	switch {
	case enc && len(ct) > 0:
		if uk == nil {
			return nil, model.ErrLocked
		}
		if plain, err = uk.Open(userID, "compactions", id, "proposal_json", ct); err != nil {
			return nil, err
		}
	case proposal.Valid && proposal.String != "":
		state, serr := st.encState(ctx, userID)
		if serr != nil {
			return nil, serr
		}
		if state == "encrypted" {
			return nil, model.ErrPlaintextRow
		}
		plain = []byte(proposal.String)
	}
	if plain != nil {
		if err := json.Unmarshal(plain, &row.Changes); err != nil {
			return nil, err
		}
	}
	return &row, nil
}
