package tenancy_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sausheong/cortex/internal/enctest"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/tenancy"
)

func TestUsagePlainPartHasNoNames(t *testing.T) {
	for _, state := range []string{"none", "encrypted"} {
		env := enctest.Open(t, state)
		ctx := context.Background()
		ops := tenancy.New(env.H)
		if _, err := ops.RefreshUsage(ctx); err != nil {
			t.Fatal(err)
		}
		var raw string
		env.DB.QueryRow(`SELECT usage_json::text FROM users WHERE id = $1`, env.UserID).Scan(&raw)
		var top map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &top); err != nil {
			t.Fatal(err)
		}
		if len(top) != 2 || top["bytes"] == nil || top["counts"] == nil {
			t.Fatalf("%s: plain usage keys = %s", state, raw)
		}
		var counts map[string]float64
		if err := json.Unmarshal(top["counts"], &counts); err != nil {
			t.Fatalf("%s: counts must be numeric only: %v", state, err)
		}
	}
}

func TestUsageDetailsEncryptedCountsPlain(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	ctx := context.Background()
	ops := tenancy.New(env.H)
	want, err := ops.RefreshUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := ops.CachedUsage(ctx)
	if err != nil || !ok || got != want {
		t.Fatalf("cached %+v %v %v want %+v", got, ok, err, want)
	}
	// A pre-encryption flat cache still reads.
	env.Exec(`UPDATE users SET usage_json = '{"bytes":7,"memories":3,"entities":2,"chunks":1,"deleted":0}' WHERE id = $1`, env.UserID)
	got, ok, _ = ops.CachedUsage(ctx)
	if !ok || got != (model.Usage{Bytes: 7, Memories: 3, Entities: 2, Chunks: 1}) {
		t.Fatalf("legacy cache = %+v", got)
	}
}
