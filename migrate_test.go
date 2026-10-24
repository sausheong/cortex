package cortex

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sausheong/cortex/internal/pgtest"
)

// rawCortex opens a Cortex without migrating, for upgrade-path tests.
func rawCortex(t *testing.T, dsn string) *Cortex {
	t.Helper()
	sdb, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sdb.Close() })
	return &Cortex{db: dbConn{sdb}, userID: BootstrapUserID}
}

func execAll(t *testing.T, c *Cortex, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := c.db.ExecContext(context.Background(), s); err != nil {
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
		if err := c.db.QueryRowContext(ctx, `SELECT count(*) FROM `+tb).Scan(&n); err != nil {
			t.Fatal(err)
		}
		before[tb] = n
	}
	if err := c.migrate(ctx); err != nil {
		t.Fatalf("migrate 002: %v", err)
	}
	for _, tb := range tables {
		var n, boot int
		if err := c.db.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE user_id = 'u_bootstrap') FROM `+tb).Scan(&n, &boot); err != nil {
			t.Fatal(err)
		}
		if n != before[tb] || boot != n {
			t.Errorf("%s: rows %d->%d, bootstrap %d", tb, before[tb], n, boot)
		}
	}
	var dims, gen int
	if err := c.db.QueryRowContext(ctx, `SELECT dims, generation FROM embeddings WHERE id = 'b1'`).Scan(&dims, &gen); err != nil || dims != 512 || gen != 1 {
		t.Fatalf("dims=%d gen=%d err=%v", dims, gen, err)
	}
	has := func(name string) bool {
		var n int
		if err := c.db.QueryRowContext(ctx,
			`SELECT count(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ?`, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n > 0
	}
	if !has("emb_hnsw_memory_512") || has("idx_embeddings_hnsw_memory") {
		t.Errorf("index conversion wrong: new=%v old=%v", has("emb_hnsw_memory_512"), has("idx_embeddings_hnsw_memory"))
	}
	var meta int
	_ = c.db.QueryRowContext(ctx, `SELECT count(*) FROM cortex_meta WHERE key = 'embedding_dims'`).Scan(&meta)
	if meta != 0 {
		t.Error("cortex_meta embedding_dims row not deleted")
	}

	// Open on the migrated DB; the bootstrap user can search its old vector.
	cx, err := Open(dsn, WithEmbedder(&dimsEmbedder{512}))
	if err != nil {
		t.Fatal(err)
	}
	defer cx.Close()
	got, err := cx.SearchMemoryVector(ctx, "tea", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "m1" {
		t.Fatalf("SearchMemoryVector = %+v", got)
	}
}

func TestMigration002SetsBootstrapOwnerEntity(t *testing.T) {
	ctx := context.Background()
	c := legacyFixture(t, pgtest.DSN(t))
	if err := c.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := c.db.QueryRowContext(ctx, `SELECT owner_entity_id FROM users WHERE id = 'u_bootstrap'`).Scan(&owner); err != nil || owner != "e1" {
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
	var typ string
	if err := c.db.QueryRowContext(ctx,
		`SELECT format_type(a.atttypid, a.atttypmod) FROM pg_attribute a
		 WHERE a.attrelid = 'embeddings'::regclass AND a.attname = 'embedding'`).Scan(&typ); err != nil || typ != "halfvec" {
		t.Fatalf("embedding type = %q err=%v, want untyped halfvec", typ, err)
	}
	var n int
	if err := c.db.QueryRowContext(ctx,
		`SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema()
		 AND table_name = 'embeddings' AND column_name IN ('dims', 'generation')`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("dims/generation columns = %d err=%v", n, err)
	}
	if err := c.db.QueryRowContext(ctx,
		`SELECT count(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname LIKE 'emb_hnsw_%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("emb_hnsw indexes = %d err=%v, want 0", n, err)
	}
	c.Close()
	c2, err := Open(dsn)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer c2.Close()
	if v := schemaVersion(t, c2); v != 8 {
		t.Errorf("version = %d", v)
	}
}

func TestMigrationNamesAreIntegers(t *testing.T) {
	re := regexp.MustCompile(`^\d+_[a-z_]+\.sql$`)
	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !re.MatchString(e.Name()) {
			t.Errorf("migration %q does not match ^\\d+_[a-z_]+\\.sql$", e.Name())
		}
	}
}

func TestBootstrapDefault(t *testing.T) {
	c := openTestDB(t)
	if c.UserID() != "u_bootstrap" {
		t.Fatalf("UserID = %q", c.UserID())
	}
	if c.ForUser("u_x").UserID() != "u_x" || c.UserID() != "u_bootstrap" {
		t.Fatal("ForUser must not mutate the receiver")
	}
}

func TestPutEntityConflictPerUser(t *testing.T) {
	ctx := context.Background()
	c := openTestDB(t)
	a, err := c.CreateUser(ctx, "sub-a", "a@x.com", "A", true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.CreateUser(ctx, "sub-b", "b@x.com", "B", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a.ID, b.ID, a.ID, b.ID} {
		if err := c.ForUser(id).PutEntity(ctx, &Entity{Type: "person", Name: "Alice"}); err != nil {
			t.Fatal(err)
		}
	}
	var n, na, nb int
	_ = c.db.QueryRowContext(ctx, `SELECT count(*) FROM entities WHERE name = 'Alice' AND deleted_at IS NULL`).Scan(&n)
	_ = c.db.QueryRowContext(ctx, `SELECT count(*) FROM entities WHERE name = 'Alice' AND user_id = ?`, a.ID).Scan(&na)
	_ = c.db.QueryRowContext(ctx, `SELECT count(*) FROM entities WHERE name = 'Alice' AND user_id = ?`, b.ID).Scan(&nb)
	if n != 2 || na != 1 || nb != 1 {
		t.Fatalf("rows total=%d a=%d b=%d, want 2/1/1", n, na, nb)
	}
	// Each user's data is invisible to the other.
	got, err := c.ForUser(a.ID).FindEntities(ctx, EntityFilter{NameLike: "Alice"})
	if err != nil || len(got) != 1 {
		t.Fatalf("user a sees %d Alice entities err=%v, want 1", len(got), err)
	}
	if _, err := c.ForUser(b.ID).GetEntity(ctx, got[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign GetEntity err = %v, want ErrNotFound", err)
	}
}

func TestSyncStatePerUser(t *testing.T) {
	ctx := context.Background()
	c := openTestDB(t)
	a, _ := c.CreateUser(ctx, "sa", "a@x.com", "A", true)
	b, _ := c.CreateUser(ctx, "sb", "b@x.com", "B", true)
	if err := c.ForUser(a.ID).SetSyncState(ctx, "files", "A-state"); err != nil {
		t.Fatal(err)
	}
	if err := c.ForUser(b.ID).SetSyncState(ctx, "files", "B-state"); err != nil {
		t.Fatal(err)
	}
	if s, _ := c.ForUser(a.ID).GetSyncState(ctx, "files"); s != "A-state" {
		t.Errorf("a = %q", s)
	}
	if s, _ := c.ForUser(b.ID).GetSyncState(ctx, "files"); s != "B-state" {
		t.Errorf("b = %q", s)
	}
	if s, _ := c.GetSyncState(ctx, "files"); s != "" {
		t.Errorf("bootstrap = %q, want empty", s)
	}
}

func TestPutEntityDoesNotDemoteOwner(t *testing.T) {
	ctx := context.Background()
	c := openTestDB(t)
	if err := c.PutEntity(ctx, &Entity{Type: "person", Name: "Sau", Source: "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := c.PutEntity(ctx, &Entity{Type: "person", Name: "Sau", Source: "extracted"}); err != nil {
		t.Fatal(err)
	}
	var src string
	if err := c.db.QueryRowContext(ctx, `SELECT source FROM entities WHERE name = 'Sau'`).Scan(&src); err != nil || src != "owner" {
		t.Fatalf("source = %q err=%v, want owner", src, err)
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

func TestMigration007FoldsDuplicateRelationships(t *testing.T) {
	ctx := context.Background()
	c := rawCortex(t, pgtest.DSN(t))
	if err := c.migrateUpTo(ctx, 6); err != nil {
		t.Fatal(err)
	}
	execAll(t, c,
		`INSERT INTO users (id) VALUES ('u1')`,
		`INSERT INTO entities (id, user_id, type, name) VALUES ('e1', 'u1', 'person', 'Ann'), ('e2', 'u1', 'organization', 'Acme')`,
		`INSERT INTO relationships (id, user_id, source_id, target_id, type, confidence, created_at) VALUES
		   ('r1', 'u1', 'e1', 'e2', 'works_at', 0.5, '2026-01-01'),
		   ('r2', 'u1', 'e1', 'e2', 'Works At', 0.9, '2026-02-01'),
		   ('r3', 'u1', 'e1', 'e2', 'works-at', 0.7, '2026-03-01'),
		   ('r4', 'u1', 'e2', 'e1', 'employs', 1.0, '2026-01-01')`,
	)
	if err := c.migrateUpTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	var n, evidence int
	var conf float64
	var last time.Time
	if err := c.db.QueryRowContext(ctx, `SELECT count(*) FROM relationships WHERE type = 'works_at'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err := c.db.QueryRowContext(ctx,
		`SELECT evidence_count, confidence, last_seen_at FROM relationships WHERE id = 'r1'`).Scan(&evidence, &conf, &last); err != nil {
		t.Fatal(err)
	}
	if n != 1 || evidence != 3 || conf != 0.9 || last.Month() != time.March {
		t.Errorf("got rows=%d evidence=%d conf=%v last=%v, want 1/3/0.9/March", n, evidence, conf, last)
	}
	if _, err := c.db.ExecContext(ctx,
		`INSERT INTO relationships (id, user_id, source_id, target_id, type) VALUES ('r5', 'u1', 'e1', 'e2', 'works_at')`); err == nil {
		t.Error("duplicate live edge inserted; want unique violation")
	}
}
