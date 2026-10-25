package integration_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	. "github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/testutil"

	"github.com/sausheong/cortex/internal/pgtest"
)

func TestMigration002FreshDB(t *testing.T) {
	ctx := context.Background()
	dsn := pgtest.DSN(t)
	c, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := testutil.H(c).DB.QueryRowContext(ctx,
		`SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema()
		 AND table_name = 'embeddings' AND column_name IN ('embedding_ct', 'embedding', 'enc')`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("embeddings carries only embedding_ct, got %d of the three err=%v", n, err)
	}
	if err := testutil.H(c).DB.QueryRowContext(ctx,
		`SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema()
		 AND table_name = 'embeddings' AND column_name IN ('dims', 'generation')`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("dims/generation columns = %d err=%v", n, err)
	}
	if err := testutil.H(c).DB.QueryRowContext(ctx,
		`SELECT count(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname LIKE 'emb_hnsw_%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("emb_hnsw indexes = %d err=%v, want 0", n, err)
	}
	c.Close()
	c2, err := Open(dsn)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer c2.Close()
	if v := schemaVersion(t, c2); v != 13 {
		t.Errorf("version = %d", v)
	}
}

func TestBootstrapDefault(t *testing.T) {
	c, err := Open(pgtest.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.UserID() != "u_bootstrap" {
		t.Fatalf("UserID = %q", c.UserID())
	}
	if c.ForUser("u_x").UserID() != "u_x" || c.UserID() != "u_bootstrap" {
		t.Fatal("ForUser must not mutate the receiver")
	}
}

func TestPutEntityConflictPerUser(t *testing.T) {
	ctx := context.Background()
	c, err := Open(pgtest.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	a, err := c.CreateUser(ctx, "sub-a", "a@x.com", "A", true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.CreateUser(ctx, "sub-b", "b@x.com", "B", true)
	if err != nil {
		t.Fatal(err)
	}
	ha, hb := keyedHandle(t, c, a.ID), keyedHandle(t, c, b.ID)
	for _, h := range []*Cortex{ha, hb, ha, hb} {
		if err := h.PutEntity(ctx, &Entity{Type: "person", Name: "Alice"}); err != nil {
			t.Fatal(err)
		}
	}
	var n, na, nb int
	_ = testutil.H(c).DB.QueryRowContext(ctx, `SELECT count(*) FROM entities WHERE deleted_at IS NULL`).Scan(&n)
	_ = testutil.H(c).DB.QueryRowContext(ctx, `SELECT count(*) FROM entities WHERE user_id = ?`, a.ID).Scan(&na)
	_ = testutil.H(c).DB.QueryRowContext(ctx, `SELECT count(*) FROM entities WHERE user_id = ?`, b.ID).Scan(&nb)
	if n != 2 || na != 1 || nb != 1 {
		t.Fatalf("rows total=%d a=%d b=%d, want 2/1/1", n, na, nb)
	}
	// Each user's data is invisible to the other.
	got, err := ha.FindEntities(ctx, EntityFilter{NameLike: "Alice"})
	if err != nil || len(got) != 1 {
		t.Fatalf("user a sees %d Alice entities err=%v, want 1", len(got), err)
	}
	if _, err := hb.GetEntity(ctx, got[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign GetEntity err = %v, want ErrNotFound", err)
	}
}

func TestSyncStatePerUser(t *testing.T) {
	ctx := context.Background()
	c, err := Open(pgtest.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	a, _ := c.CreateUser(ctx, "sa", "a@x.com", "A", true)
	b, _ := c.CreateUser(ctx, "sb", "b@x.com", "B", true)
	ha, hb := keyedHandle(t, c, a.ID), keyedHandle(t, c, b.ID)
	if err := ha.SetSyncState(ctx, "files", "A-state"); err != nil {
		t.Fatal(err)
	}
	if err := hb.SetSyncState(ctx, "files", "B-state"); err != nil {
		t.Fatal(err)
	}
	if s, _ := ha.GetSyncState(ctx, "files"); s != "A-state" {
		t.Errorf("a = %q", s)
	}
	if s, _ := hb.GetSyncState(ctx, "files"); s != "B-state" {
		t.Errorf("b = %q", s)
	}
}

func TestResolveUserRef(t *testing.T) {
	ctx := context.Background()
	c := openTestDB(t)
	solo, _ := c.CreateUser(ctx, "s1", "solo@x.com", "Solo", true)
	v, _ := c.CreateUser(ctx, "s2", "dup@x.com", "V", true)
	u, _ := c.CreateUser(ctx, "s3", "dup@x.com", "U", false)
	v1, _ := c.CreateUser(ctx, "s4", "both@x.com", "B1", true)
	v2, _ := c.CreateUser(ctx, "s5", "both@x.com", "B2", true)
	u1, _ := c.CreateUser(ctx, "s6", "unv@x.com", "N1", false)
	u2, _ := c.CreateUser(ctx, "s7", "unv@x.com", "N2", false)

	if got, err := c.ResolveUserRef(ctx, solo.ID); err != nil || got.ID != solo.ID {
		t.Errorf("by ID: %v %v", got, err)
	}
	if got, err := c.ResolveUserRef(ctx, "solo@x.com"); err != nil || got.ID != solo.ID {
		t.Errorf("by verified email: %v %v", got, err)
	}
	if got, err := c.ResolveUserRef(ctx, "dup@x.com"); err != nil || got.ID != v.ID {
		t.Errorf("one verified of two: %v %v (unverified %s)", got, err, u.ID)
	}
	for _, tc := range []struct {
		ref string
		ids []string
	}{{"both@x.com", []string{v1.ID, v2.ID}}, {"unv@x.com", []string{u1.ID, u2.ID}}} {
		_, err := c.ResolveUserRef(ctx, tc.ref)
		if err == nil {
			t.Fatalf("%s: want error", tc.ref)
		}
		for _, id := range tc.ids {
			if !strings.Contains(err.Error(), id) {
				t.Errorf("%s: error %q should list %s", tc.ref, err, id)
			}
		}
	}
	if _, err := c.ResolveUserRef(ctx, "nobody@x.com"); err != ErrNotFound {
		t.Errorf("unknown: %v, want ErrNotFound", err)
	}
}
