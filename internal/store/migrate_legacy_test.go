package store

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/sausheong/cortex/internal/pgtest"
)

func vectorLiteral(v []float32) string {
	parts := make([]string, len(v))
	for i, x := range v {
		parts[i] = strconv.FormatFloat(float64(x), 'g', -1, 32)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func vec512Literal() string {
	v := make([]float32, 512)
	v[0] = 1
	return vectorLiteral(v)
}

// legacyFixture brings a DB to the pre-002 state of a deployed instance.
func legacyFixture(t *testing.T, dsn string) *Handle {
	t.Helper()
	ctx := context.Background()
	c := rawHandle(t, dsn)
	if err := c.MigrateUpTo(ctx, 1); err != nil {
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
		if err := c.DB.QueryRowContext(ctx, `SELECT count(*) FROM `+tb).Scan(&n); err != nil {
			t.Fatal(err)
		}
		before[tb] = n
	}
	// Migration 012 refuses while an unenrolled user holds data (this fixture
	// is a pre-encryption deployment), so stop before it.
	if err := c.MigrateUpTo(ctx, 11); err != nil {
		t.Fatalf("migrate 002: %v", err)
	}
	for _, tb := range tables {
		var n, boot int
		if err := c.DB.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE user_id = 'u_bootstrap') FROM `+tb).Scan(&n, &boot); err != nil {
			t.Fatal(err)
		}
		if n != before[tb] || boot != n {
			t.Errorf("%s: rows %d->%d, bootstrap %d", tb, before[tb], n, boot)
		}
	}
	var dims, gen int
	if err := c.DB.QueryRowContext(ctx, `SELECT dims, generation FROM embeddings WHERE id = 'b1'`).Scan(&dims, &gen); err != nil || dims != 512 || gen != 1 {
		t.Fatalf("dims=%d gen=%d err=%v", dims, gen, err)
	}
	has := func(name string) bool {
		var n int
		if err := c.DB.QueryRowContext(ctx,
			`SELECT count(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ?`, name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n > 0
	}
	if !has("emb_hnsw_memory_512") || has("idx_embeddings_hnsw_memory") {
		t.Errorf("index conversion wrong: new=%v old=%v", has("emb_hnsw_memory_512"), has("idx_embeddings_hnsw_memory"))
	}
	var meta int
	_ = c.DB.QueryRowContext(ctx, `SELECT count(*) FROM cortex_meta WHERE key = 'embedding_dims'`).Scan(&meta)
	if meta != 0 {
		t.Error("cortex_meta embedding_dims row not deleted")
	}

}

func TestMigration002SetsBootstrapOwnerEntity(t *testing.T) {
	ctx := context.Background()
	c := legacyFixture(t, pgtest.DSN(t))
	if err := c.MigrateUpTo(ctx, 11); err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := c.DB.QueryRowContext(ctx, `SELECT owner_entity_id FROM users WHERE id = 'u_bootstrap'`).Scan(&owner); err != nil || owner != "e1" {
		t.Fatalf("owner_entity_id = %q err=%v, want e1", owner, err)
	}
}
