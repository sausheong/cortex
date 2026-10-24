package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sausheong/cortex/internal/enctest"
	"github.com/sausheong/cortex/internal/model"
)

func TestSyncStateEncryptedAtRest(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	ctx := context.Background()
	if err := env.H.SetSyncState(ctx, "gmail", `{"cursor":"CURSORSECRET"}`); err != nil {
		t.Fatal(err)
	}
	if err := env.H.SetSyncState(ctx, "gmail", `{"cursor":"CURSORSECRET2"}`); err != nil {
		t.Fatal(err)
	}
	var raw string
	env.DB.QueryRow(`SELECT to_jsonb(t)::text FROM sync_state t WHERE user_id = $1`, env.UserID).Scan(&raw)
	if strings.Contains(raw, "CURSORSECRET") {
		t.Fatalf("sync state leaks: %s", raw)
	}
	if env.Count(`SELECT count(*) FROM sync_state WHERE user_id = $1 AND state_ct IS NOT NULL`, env.UserID) != 1 {
		t.Fatal("row not sealed")
	}
	got, err := env.H.GetSyncState(ctx, "gmail")
	if err != nil || got != `{"cursor":"CURSORSECRET2"}` {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := env.Keyless.GetSyncState(ctx, "gmail"); err != model.ErrLocked {
		t.Fatalf("keyless read = %v", err)
	}
	if err := env.Keyless.SetSyncState(ctx, "gmail", "x"); err != model.ErrLocked {
		t.Fatalf("keyless write = %v", err)
	}
}

// An unenrolled user has no key, so nothing is stored for them.
func TestSyncStateRequiresEnrollment(t *testing.T) {
	env := enctest.Open(t, "none")
	if err := env.Keyless.SetSyncState(context.Background(), "gmail", "plain"); !errors.Is(err, model.ErrEnrollRequired) {
		t.Fatalf("SetSyncState = %v, want ErrEnrollRequired", err)
	}
}
