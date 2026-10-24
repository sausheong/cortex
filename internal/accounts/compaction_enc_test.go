package accounts

import (
	"errors"
	"strings"
	"testing"

	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/model"
)

func TestCompactionProposalEncryptedAtRest(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	uid := e.user(t, "cmp")
	if _, err := e.st.DB.ExecContext(ctx, `UPDATE users SET encryption_state = 'encrypted' WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	uk, _ := keys.NewUK()
	defer uk.Zero()
	if _, err := e.st.DB.ExecContext(ctx, `INSERT INTO compactions (id, user_id, status) VALUES ('c_1', $1, 'proposing')`, uid); err != nil {
		t.Fatal(err)
	}
	ch := []model.Change{{Kind: "merge", SourceIDs: []string{"m1", "m2"}, Result: "MERGEDSECRET result", Sources: []string{"SRCSECRET one", "SRCSECRET two"}}}
	if err := e.st.StoreCompactionProposal(ctx, uid, "c_1", uk, ch); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := e.st.DB.QueryRowContext(ctx, `SELECT to_jsonb(t)::text FROM compactions t WHERE id = 'c_1'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "SECRET") || strings.Contains(raw, "m1") {
		t.Fatalf("compaction row leaks: %s", raw)
	}
	row, err := e.st.LoadCompaction(ctx, uid, "c_1", uk)
	if err != nil || row.Status != "ready" || len(row.Changes) != 1 || row.Changes[0].Result != "MERGEDSECRET result" || row.Changes[0].Sources[1] != "SRCSECRET two" {
		t.Fatalf("round trip: %+v %v", row, err)
	}
	if _, err := e.st.LoadCompaction(ctx, uid, "c_1", nil); err != model.ErrLocked {
		t.Fatalf("keyless load = %v", err)
	}
	// Status-only views still work without a key.
	if err := e.st.StoreCompactionProposal(ctx, uid, "c_1", nil, ch); err != model.ErrLocked {
		t.Fatalf("keyless store = %v", err)
	}
	// An unenrolled user has no key to seal with.
	lu := e.user(t, "unenrolled")
	e.st.DB.ExecContext(ctx, `INSERT INTO compactions (id, user_id, status) VALUES ('c_2', $1, 'proposing')`, lu)
	if err := e.st.StoreCompactionProposal(ctx, lu, "c_2", nil, ch); !errors.Is(err, model.ErrEnrollRequired) {
		t.Fatalf("unenrolled store = %v", err)
	}
}
