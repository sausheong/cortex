package store

import (
	"context"
	"database/sql"
	"os"
	"regexp"
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
