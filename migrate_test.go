package cortex

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/sausheong/cortex/internal/pgtest"
	"github.com/sausheong/cortex/internal/store"
)

// rawCortex opens a Cortex without migrating, for upgrade-path tests.
func rawCortex(t *testing.T, dsn string) *Cortex {
	t.Helper()
	sdb, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sdb.Close() })
	return &Cortex{h: &store.Handle{DB: store.DB{DB: sdb}, UserID: BootstrapUserID}}
}

func execAll(t *testing.T, c *Cortex, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := c.h.DB.ExecContext(context.Background(), s); err != nil {
			t.Fatalf("exec %q: %v", s, err)
		}
	}
}

func vec512Literal() string {
	v := make([]float32, 512)
	v[0] = 1
	return vectorLiteral(v)
}

// legacyFixture brings a DB to the pre-002 state of a deployed instance.
func legacyFixture(t *testing.T, dsn string) *Cortex {
	t.Helper()
	ctx := context.Background()
	c := rawCortex(t, dsn)
	if err := c.migrateUpTo(ctx, 1); err != nil {
		t.Fatal(err)
	}
	execAll(t, c,
		`ALTER TABLE embeddings ADD COLUMN embedding halfvec(512) NOT NULL`,
		`CREATE INDEX idx_embeddings_hnsw_entity ON embeddings USING hnsw (embedding halfvec_cosine_ops) WHERE ref_type = 'entity'`,
		`CREATE INDEX idx_embeddings_hnsw_memory ON embeddings USING hnsw (embedding halfvec_cosine_ops) WHERE ref_type = 'memory'`,
		`CREATE INDEX idx_embeddings_hnsw_chunk ON embeddings USING hnsw (embedding halfvec_cosine_ops) WHERE ref_type = 'chunk'`,
		`INSERT INTO cortex_meta (key, value) VALUES ('embedding_dims', '512')`,
		`INSERT INTO entities (id, type, name, source) VALUES ('e1', 'person', 'Sau', 'owner'), ('e2', 'person', 'Bob', 'x')`,
		`INSERT INTO relationships (id, source_id, target_id, type) VALUES ('r1', 'e1', 'e2', 'knows')`,
		`INSERT INTO memories (id, content, source) VALUES ('m1', 'Sau likes tea', 's'), ('m2', 'Bob likes coffee', 's')`,
		`INSERT INTO memory_entities (memory_id, entity_id) VALUES ('m1', 'e1')`,
		`INSERT INTO memory_edges (id, source_id, target_id, type) VALUES ('x1', 'm1', 'm2', 'extends')`,
		`INSERT INTO chunks (id, entity_id, content) VALUES ('c1', 'e1', 'a chunk')`,
		`INSERT INTO embeddings (id, ref_id, ref_type, embedding) VALUES ('b1', 'm1', 'memory', '`+vec512Literal()+`')`,
		`INSERT INTO sync_state (connector, state) VALUES ('files', '{}')`,
	)
	return c
}

func TestMigration002FromLegacy(t *testing.T) {
	ctx := context.Background()
	dsn := pgtest.DSN(t)
	c := legacyFixture(t, dsn)
	tables := []string{"entities", "relationships", "memories", "memory_entities", "memory_edges", "chunks", "embeddings", "sync_state"}
	before := map[string]int{}
	for _, tb := range tables {
		var n int
		if err := c.h.DB.QueryRowContext(ctx, `SELECT count(*) FROM `+tb).Scan(&n); err != nil {
			t.Fatal(err)
		}
		before[tb] = n
	}
	// Migration 012 refuses while an unenrolled user holds data (this fixture
	// is a pre-encryption deployment), so stop before it.
	if err := c.migrateUpTo(ctx, 11); err != nil {
		t.Fatalf("migrate 002: %v", err)
	}
	for _, tb := range tables {
		var n, boot int
		if err := c.h.DB.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE user_id = 'u_bootstrap') FROM `+tb).Scan(&n, &boot); err != nil {
			t.Fatal(err)
		}
		if n != before[tb] || boot != n {
			t.Errorf("%s: rows %d->%d, bootstrap %d", tb, before[tb], n, boot)
		}
	}
	var dims, gen int
	if err := c.h.DB.QueryRowContext(ctx, `SELECT dims, generation FROM embeddings WHERE id = 'b1'`).Scan(&dims, &gen); err != nil || dims != 512 || gen != 1 {
		t.Fatalf("dims=%d gen=%d err=%v", dims, gen, err)
	}
	has := func(name string) bool {
		var n int
		if err := c.h.DB.QueryRowContext(ctx,
			`SELECT count(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ?`, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n > 0
	}
	if !has("emb_hnsw_memory_512") || has("idx_embeddings_hnsw_memory") {
		t.Errorf("index conversion wrong: new=%v old=%v", has("emb_hnsw_memory_512"), has("idx_embeddings_hnsw_memory"))
	}
	var meta int
	_ = c.h.DB.QueryRowContext(ctx, `SELECT count(*) FROM cortex_meta WHERE key = 'embedding_dims'`).Scan(&meta)
	if meta != 0 {
		t.Error("cortex_meta embedding_dims row not deleted")
	}

}

func TestMigration002SetsBootstrapOwnerEntity(t *testing.T) {
	ctx := context.Background()
	c := legacyFixture(t, pgtest.DSN(t))
	if err := c.migrateUpTo(ctx, 11); err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := c.h.DB.QueryRowContext(ctx, `SELECT owner_entity_id FROM users WHERE id = 'u_bootstrap'`).Scan(&owner); err != nil || owner != "e1" {
		t.Fatalf("owner_entity_id = %q err=%v, want e1", owner, err)
	}
}

func TestMigration002FreshDB(t *testing.T) {
	ctx := context.Background()
	dsn := pgtest.DSN(t)
	c, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := c.h.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema()
		 AND table_name = 'embeddings' AND column_name IN ('embedding_ct', 'embedding', 'enc')`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("embeddings carries only embedding_ct, got %d of the three err=%v", n, err)
	}
	if err := c.h.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema()
		 AND table_name = 'embeddings' AND column_name IN ('dims', 'generation')`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("dims/generation columns = %d err=%v", n, err)
	}
	if err := c.h.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname LIKE 'emb_hnsw_%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("emb_hnsw indexes = %d err=%v, want 0", n, err)
	}
	c.Close()
	c2, err := Open(dsn)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer c2.Close()
	if v := schemaVersion(t, c2); v != 12 {
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
	_ = c.h.DB.QueryRowContext(ctx, `SELECT count(*) FROM entities WHERE deleted_at IS NULL`).Scan(&n)
	_ = c.h.DB.QueryRowContext(ctx, `SELECT count(*) FROM entities WHERE user_id = ?`, a.ID).Scan(&na)
	_ = c.h.DB.QueryRowContext(ctx, `SELECT count(*) FROM entities WHERE user_id = ?`, b.ID).Scan(&nb)
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
