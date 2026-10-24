package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/pgtest"
)

type fixedEmb struct{}

func (fixedEmb) Dimensions() int { return 512 }
func (fixedEmb) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, 512)
		v[i%512] = 1
		out[i] = v
	}
	return out, nil
}

func adminRoot(t *testing.T) (*cortex.Cortex, string) {
	t.Helper()
	dsn := pgtest.DSN(t)
	root, err := cortex.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root, dsn
}

func TestStripUserFlag(t *testing.T) {
	ref, rest, err := stripUserFlag([]string{"--rerank", "--user", "u_1", "q"})
	if err != nil || ref != "u_1" || strings.Join(rest, " ") != "--rerank q" {
		t.Fatalf("%q %v %v", ref, rest, err)
	}
	if _, _, err := stripUserFlag([]string{"--user"}); err == nil {
		t.Fatal("dangling --user accepted")
	}
	for _, c := range []string{"maintain", "recall", "profile", "import-sqlite", "sync"} {
		if !userCommands[c] {
			t.Fatalf("%s must accept --user", c)
		}
	}
	if userCommands["serve"] || userCommands["forget"] {
		t.Fatal("--user accepted on an unsupported command")
	}
}

func TestUsersListCommand(t *testing.T) {
	root, _ := adminRoot(t)
	ctx := t.Context()
	a, err := root.CreateUser(ctx, "sa", "a@x.test", "A", true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := root.CreateUser(ctx, "sb", "b@x.test", "B", false)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := listUsers(ctx, root, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{a.ID, b.ID, cortex.BootstrapUserID, "a@x.test (verified)", "b@x.test (unverified)", "memories"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestUserFlagByIDAndVerifiedEmail(t *testing.T) {
	root, _ := adminRoot(t)
	ctx := t.Context()
	u, err := root.CreateUser(ctx, "sa", "ok@x.test", "A", true)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := resolveUser(ctx, root, u.ID); err != nil || got.ID != u.ID {
		t.Fatalf("by id: %v %v", got, err)
	}
	if got, err := resolveUser(ctx, root, "ok@x.test"); err != nil || got.ID != u.ID {
		t.Fatalf("by email: %v %v", got, err)
	}
	if _, err := resolveUser(ctx, root, "nobody@x.test"); err == nil || !strings.Contains(err.Error(), "no user matches") {
		t.Fatalf("no match: %v", err)
	}
}

func TestUserFlagAmbiguousRefusesListsIDs(t *testing.T) {
	root, _ := adminRoot(t)
	ctx := t.Context()
	a, _ := root.CreateUser(ctx, "s1", "dup@x.test", "A", true)
	b, _ := root.CreateUser(ctx, "s2", "dup@x.test", "B", true)
	_, err := resolveUser(ctx, root, "dup@x.test")
	if err == nil || !strings.Contains(err.Error(), a.ID) || !strings.Contains(err.Error(), b.ID) {
		t.Fatalf("ambiguity must list both IDs: %v", err)
	}
}

func TestUserFlagUnverifiedEmailRejected(t *testing.T) {
	root, _ := adminRoot(t)
	ctx := t.Context()
	u, _ := root.CreateUser(ctx, "s1", "unv@x.test", "A", false)
	_, err := resolveUser(ctx, root, "unv@x.test")
	if err == nil || !strings.Contains(err.Error(), u.ID) {
		t.Fatalf("unverified email must be refused with the ID listed: %v", err)
	}
}

func TestUserFlagDeletedAndMissingSecret(t *testing.T) {
	root, dsn := adminRoot(t)
	ctx := t.Context()
	u, _ := root.CreateUser(ctx, "s1", "del@x.test", "A", true)
	if _, err := openUserCortex(ctx, dsn, u.ID, map[string]string{}); err == nil || !strings.Contains(err.Error(), "CORTEX_SECRET_KEY") {
		t.Fatalf("want secret key error: %v", err)
	}
	db, _ := sql.Open("pgx", dsn)
	defer db.Close()
	if _, err := db.Exec(`UPDATE users SET deleted_at = now() WHERE id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveUser(ctx, root, u.ID); err == nil || !strings.Contains(err.Error(), "deleted") {
		t.Fatalf("deleted user accepted: %v", err)
	}
}

func TestImportSqliteUserFlag(t *testing.T) {
	root, _ := adminRoot(t)
	ctx := t.Context()
	u, err := root.CreateUser(ctx, "imp", "imp@x.test", "Imp", true)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("../../internal/sqliteimport/testdata/v040_schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "old.db")
	sdb, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{string(schema),
		`INSERT INTO entities (id,type,name,attributes,source,created_at,updated_at,confidence) VALUES ('e1','person','Alice','{}','s','2026-03-01 08:00:00','2026-03-01 08:00:00',0.9)`,
		`INSERT INTO memories (id,content,source,created_at,updated_at,valid_at,static,speaker) VALUES ('m1','Alice likes tea','s','2026-03-01 08:00:00','2026-03-01 08:00:00','2026-03-01 08:00:00',0,NULL)`,
	} {
		if _, err := sdb.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	sdb.Close()

	cx := root.ForUser(u.ID, cortex.WithEmbedder(fixedEmb{}))
	var out, errw bytes.Buffer
	if err := runImportSQLite(ctx, cx, importSQLiteOptions{Path: path}, &out, &errw); err != nil {
		t.Fatalf("import: %v\n%s", err, out.String())
	}
	count := func(user string) int {
		var n int
		if err := root.DB().QueryRow(`SELECT (SELECT count(*) FROM entities WHERE user_id=$1 AND id='e1') + (SELECT count(*) FROM memories WHERE user_id=$1 AND id='m1')`, user).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count(u.ID) != 2 {
		t.Fatalf("rows missing under the resolved user: %d", count(u.ID))
	}
	if count(cortex.BootstrapUserID) != 0 {
		t.Fatal("rows leaked into the bootstrap user")
	}
}

func TestMigrateCommandReportsVersions(t *testing.T) {
	dsn := pgtest.DSN(t)
	var buf bytes.Buffer
	if err := runMigrate(dsn, 2*time.Minute, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "[1 2 3 4 5 6 7 8]") || !strings.Contains(out, "elapsed:") {
		t.Fatalf("output: %q", out)
	}
}
