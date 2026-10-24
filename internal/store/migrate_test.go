package store

import (
	"context"
	"database/sql"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/pgtest"
)

// rawHandle opens a Handle without migrating, for upgrade-path tests.
func rawHandle(t *testing.T, dsn string) *Handle {
	t.Helper()
	sdb, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sdb.Close() })
	return &Handle{DB: DB{sdb}, UserID: model.BootstrapUserID}
}

func execAll(t *testing.T, h *Handle, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := h.DB.ExecContext(context.Background(), s); err != nil {
			t.Fatalf("exec %q: %v", s, err)
		}
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

func TestMigration007FoldsDuplicateRelationships(t *testing.T) {
	ctx := context.Background()
	h := rawHandle(t, pgtest.DSN(t))
	if err := h.MigrateUpTo(ctx, 6); err != nil {
		t.Fatal(err)
	}
	execAll(t, h,
		`INSERT INTO users (id) VALUES ('u1')`,
		`INSERT INTO entities (id, user_id, type, name) VALUES ('e1', 'u1', 'person', 'Ann'), ('e2', 'u1', 'organization', 'Acme')`,
		`INSERT INTO relationships (id, user_id, source_id, target_id, type, confidence, created_at) VALUES
		   ('r1', 'u1', 'e1', 'e2', 'works_at', 0.5, '2026-01-01'),
		   ('r2', 'u1', 'e1', 'e2', 'Works At', 0.9, '2026-02-01'),
		   ('r3', 'u1', 'e1', 'e2', 'works-at', 0.7, '2026-03-01'),
		   ('r4', 'u1', 'e2', 'e1', 'employs', 1.0, '2026-01-01')`,
	)
	if err := h.MigrateUpTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	var n, evidence int
	var conf float64
	var last time.Time
	if err := h.DB.QueryRowContext(ctx, `SELECT count(*) FROM relationships WHERE type = 'works_at'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err := h.DB.QueryRowContext(ctx,
		`SELECT evidence_count, confidence, last_seen_at FROM relationships WHERE id = 'r1'`).Scan(&evidence, &conf, &last); err != nil {
		t.Fatal(err)
	}
	if n != 1 || evidence != 3 || conf != 0.9 || last.Month() != time.March {
		t.Errorf("got rows=%d evidence=%d conf=%v last=%v, want 1/3/0.9/March", n, evidence, conf, last)
	}
	if _, err := h.DB.ExecContext(ctx,
		`INSERT INTO relationships (id, user_id, source_id, target_id, type) VALUES ('r5', 'u1', 'e1', 'e2', 'works_at')`); err == nil {
		t.Error("duplicate live edge inserted; want unique violation")
	}
}

func migrated(t *testing.T) *Handle {
	t.Helper()
	h := rawHandle(t, pgtest.DSN(t))
	if err := h.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	execAll(t, h, `INSERT INTO users (id) VALUES ('u1'), ('u2')`)
	return h
}

func mustFail(t *testing.T, h *Handle, what string, q string, args ...any) {
	t.Helper()
	if _, err := h.DB.ExecContext(context.Background(), q, args...); err == nil {
		t.Errorf("%s: want error, got none", what)
	}
}

func mustOK(t *testing.T, h *Handle, what string, q string, args ...any) {
	t.Helper()
	if _, err := h.DB.ExecContext(context.Background(), q, args...); err != nil {
		t.Errorf("%s: %v", what, err)
	}
}

func TestMigration010Applies(t *testing.T) {
	ctx := context.Background()
	h := migrated(t)
	for _, tbl := range []string{"user_key_wraps", "webauthn_credentials", "webauthn_ceremonies"} {
		var n int
		if err := h.DB.QueryRowContext(ctx,
			`SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = ?`, tbl).Scan(&n); err != nil || n != 1 {
			t.Errorf("table %s missing (%d, %v)", tbl, n, err)
		}
	}
	mustOK(t, h, "encrypted entity with NULL name", `INSERT INTO entities (id, user_id, enc, name_blind, type_blind) VALUES ('e1','u1',true,'\x01','\x02')`)
	mustFail(t, h, "plain entity with NULL name", `INSERT INTO entities (id, user_id, enc, type) VALUES ('e2','u1',false,'person')`)
	mustFail(t, h, "equal blinds", `INSERT INTO entities (id, user_id, enc, name_blind, type_blind) VALUES ('e3','u1',true,'\x01','\x02')`)
	mustOK(t, h, "different blinds", `INSERT INTO entities (id, user_id, enc, name_blind, type_blind) VALUES ('e4','u1',true,'\x03','\x02')`)
	mustOK(t, h, "legacy 1", `INSERT INTO entities (id, user_id, name, type) VALUES ('l1','u1','Ann','person')`)
	mustFail(t, h, "legacy dup", `INSERT INTO entities (id, user_id, name, type) VALUES ('l2','u1','Ann','person')`)
	mustOK(t, h, "encrypted users", `UPDATE users SET encryption_state = 'migrating' WHERE id = 'u1'`)
	mustFail(t, h, "bad state", `UPDATE users SET encryption_state = 'bogus' WHERE id = 'u1'`)
}

func TestRelationshipUniqueEncryptedDedup(t *testing.T) {
	h := migrated(t)
	execAll(t, h,
		`INSERT INTO entities (id, user_id, name, type) VALUES ('a','u1','A','person'), ('b','u1','B','person')`)
	mustOK(t, h, "enc 1", `INSERT INTO relationships (id, user_id, source_id, target_id, enc, type_blind) VALUES ('r1','u1','a','b',true,'\x01')`)
	mustFail(t, h, "enc dup", `INSERT INTO relationships (id, user_id, source_id, target_id, enc, type_blind) VALUES ('r2','u1','a','b',true,'\x01')`)
	mustOK(t, h, "enc other type", `INSERT INTO relationships (id, user_id, source_id, target_id, enc, type_blind) VALUES ('r3','u1','a','b',true,'\x02')`)
	mustOK(t, h, "legacy alongside", `INSERT INTO relationships (id, user_id, source_id, target_id, type) VALUES ('r4','u1','a','b','knows')`)
	mustFail(t, h, "legacy dup", `INSERT INTO relationships (id, user_id, source_id, target_id, type) VALUES ('r5','u1','a','b','knows')`)
}

func TestAliasCrossEntityUniqueness(t *testing.T) {
	h := migrated(t)
	execAll(t, h,
		`INSERT INTO entities (id, user_id, name, type) VALUES ('a','u1','A','person'), ('b','u1','B','person')`)
	mustOK(t, h, "alias 1", `INSERT INTO entity_aliases (user_id, entity_id, kind, enc, value_blind) VALUES ('u1','a','email',true,'\x09')`)
	mustFail(t, h, "same blind other entity", `INSERT INTO entity_aliases (user_id, entity_id, kind, enc, value_blind) VALUES ('u1','b','email',true,'\x09')`)
	mustOK(t, h, "other kind", `INSERT INTO entity_aliases (user_id, entity_id, kind, enc, value_blind) VALUES ('u1','b','phone',true,'\x09')`)
	mustOK(t, h, "legacy", `INSERT INTO entity_aliases (user_id, entity_id, kind, value, value_norm) VALUES ('u1','a','email','X@y','x@y')`)
	mustFail(t, h, "legacy cross entity", `INSERT INTO entity_aliases (user_id, entity_id, kind, value, value_norm) VALUES ('u1','b','email','x@Y','x@y')`)
}

func TestAliasEncryptedRowWithNullValueNormInserts(t *testing.T) {
	h := migrated(t)
	execAll(t, h, `INSERT INTO entities (id, user_id, name, type) VALUES ('a','u1','A','person')`)
	mustOK(t, h, "null value/value_norm", `INSERT INTO entity_aliases (user_id, entity_id, kind, enc, value_ct, value_blind) VALUES ('u1','a','email',true,'\x01','\x02')`)
	mustFail(t, h, "legacy with null value_norm", `INSERT INTO entity_aliases (user_id, entity_id, kind, value) VALUES ('u1','a','email','x')`)
	var id string
	if err := h.DB.QueryRowContext(context.Background(), `SELECT id FROM entity_aliases WHERE enc`).Scan(&id); err != nil || id == "" {
		t.Errorf("alias id not defaulted: %q %v", id, err)
	}
}

func TestIsOwnerBackfilled(t *testing.T) {
	ctx := context.Background()
	h := rawHandle(t, pgtest.DSN(t))
	if err := h.MigrateUpTo(ctx, 9); err != nil {
		t.Fatal(err)
	}
	execAll(t, h,
		`INSERT INTO users (id) VALUES ('u1')`,
		`INSERT INTO entities (id, user_id, type, name, source) VALUES ('o','u1','person','Me','owner'), ('x','u1','person','You','ingest')`,
		`INSERT INTO entity_aliases (user_id, entity_id, kind, value, value_norm) VALUES ('u1','o','name','Me','me')`)
	if err := h.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var owner, other bool
	h.DB.QueryRowContext(ctx, `SELECT is_owner FROM entities WHERE id='o'`).Scan(&owner)
	h.DB.QueryRowContext(ctx, `SELECT is_owner FROM entities WHERE id='x'`).Scan(&other)
	if !owner || other {
		t.Errorf("is_owner owner=%v other=%v", owner, other)
	}
	var id string
	h.DB.QueryRowContext(ctx, `SELECT id FROM entity_aliases`).Scan(&id)
	if id == "" {
		t.Error("alias id not backfilled")
	}
}

func TestLegacyBtreeIndexesArePartial(t *testing.T) {
	h := migrated(t)
	for _, name := range []string{"idx_entities_type", "idx_entities_name", "idx_entities_source",
		"idx_relationships_type", "idx_entity_aliases_value", "idx_entities_user_name_type",
		"idx_relationships_unique_live", "idx_entity_aliases_legacy_uniq"} {
		var def string
		if err := h.DB.QueryRowContext(context.Background(),
			`SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ?`, name).Scan(&def); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !strings.Contains(def, "enc IS NOT TRUE") {
			t.Errorf("%s not partial: %s", name, def)
		}
	}
}

func TestEncryptionRequiredSinceMarkerSet(t *testing.T) {
	h := migrated(t)
	var v string
	if err := h.DB.QueryRowContext(context.Background(),
		`SELECT value FROM cortex_meta WHERE key = 'encryption_required_since'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if _, err := time.Parse("2006-01-02 15:04:05.999999999-07", v); err != nil {
		t.Errorf("marker %q not a timestamp: %v", v, err)
	}
}

func TestMigration011MakesSearchIndexesPartial(t *testing.T) {
	ctx := context.Background()
	h := rawHandle(t, pgtest.DSN(t))
	if err := h.MigrateUpTo(ctx, 10); err != nil {
		t.Fatal(err)
	}
	// An existing deployment: a non-partial per-dimension HNSW index.
	execAll(t, h, `CREATE INDEX emb_hnsw_memory_8 ON embeddings USING hnsw ((embedding::halfvec(8)) halfvec_cosine_ops) WHERE ref_type = 'memory' AND dims = 8`)
	if err := h.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, idx := range []string{"idx_memories_tsv", "idx_chunks_tsv", "emb_hnsw_memory_8"} {
		var def string
		if err := h.DB.QueryRowContext(ctx,
			`SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ?`, idx).Scan(&def); err != nil {
			t.Fatalf("%s: %v", idx, err)
		}
		if !strings.Contains(def, "enc IS NOT TRUE") {
			t.Errorf("%s is not partial: %s", idx, def)
		}
	}
	// Idempotent: a second run leaves the partial index alone.
	if err := h.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
}
