package sqliteimport

import (
	"context"
	"testing"

	"github.com/sausheong/cortex"
)

// TestIsolationImport imports into user a while user b already holds data:
// the importer must create nothing for b (or the bootstrap user) and its
// report must count only a's rows.
func TestIsolationImport(t *testing.T) {
	ctx := context.Background()
	path := makeSQLite(t, "2026-03-01 08:00:00")
	cx := openTarget(t, &fakeEmb{})
	// User a is inserted directly: CreateUser would add an owner entity and
	// the importer refuses a non-empty target.
	aID := "u_import_a"
	if _, err := cx.DB().Exec(`INSERT INTO users (id, email, name, email_verified) VALUES ($1, 'a@example.com', 'A', true)`, aID); err != nil {
		t.Fatal(err)
	}
	a := struct{ ID string }{aID}
	b, err := cx.CreateUser(ctx, "sub-b", "b@example.com", "B", true)
	if err != nil {
		t.Fatal(err)
	}
	// b already has an entity: a's "target not empty" check must ignore it.
	if err := cx.ForUser(b.ID).PutEntity(ctx, &cortex.Entity{Type: "person", Name: "Bea"}); err != nil {
		t.Fatal(err)
	}
	countFor := func(userID string) map[string]int {
		out := map[string]int{}
		for _, tbl := range []string{"entities", "relationships", "memories", "memory_entities", "memory_edges", "chunks", "sync_state", "embeddings"} {
			var n int
			if err := cx.DB().QueryRow(`SELECT count(*) FROM `+tbl+` WHERE user_id = $1`, userID).Scan(&n); err != nil {
				t.Fatal(err)
			}
			out[tbl] = n
		}
		return out
	}
	beforeB := countFor(b.ID)
	beforeBoot := countFor(cortex.BootstrapUserID)

	rep, err := Import(ctx, path, cx, a.ID, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for tbl, p := range rep.Counts {
		if p[0] != p[1] || p[0] == 0 {
			t.Errorf("%s: source %d target %d", tbl, p[0], p[1])
		}
	}
	gotA := countFor(a.ID)
	for tbl, p := range rep.Counts {
		if gotA[tbl] != p[1] {
			t.Errorf("%s: user a has %d rows, report says %d", tbl, gotA[tbl], p[1])
		}
	}
	if gotA["embeddings"] == 0 || rep.Embedded != gotA["embeddings"] {
		t.Errorf("embedded %d, a has %d embeddings", rep.Embedded, gotA["embeddings"])
	}
	for tbl, n := range countFor(b.ID) {
		if n != beforeB[tbl] {
			t.Errorf("b.%s changed %d -> %d", tbl, beforeB[tbl], n)
		}
	}
	for tbl, n := range countFor(cortex.BootstrapUserID) {
		if n != beforeBoot[tbl] {
			t.Errorf("bootstrap.%s changed %d -> %d", tbl, beforeBoot[tbl], n)
		}
	}
}
