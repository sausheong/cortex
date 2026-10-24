package cortex

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sausheong/cortex/internal/pgtest"
)

// isoWorld is two users with identically named content.
type isoWorld struct {
	a, b           *Cortex
	aEnt, bEnt     *Entity
	aEnt2, bEnt2   *Entity
	aMem, bMem     *Memory
	aMem2, bMem2   *Memory
	aChunk, bChunk *Chunk
}

// twoUsers returns handles for two fresh users sharing one database.
func twoUsers(t *testing.T) (a, b *Cortex) {
	t.Helper()
	root, err := Open(pgtest.DSN(t), WithEmbedder(&testEmbedder{}))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { root.Close() })
	ctx := context.Background()
	ua, err := root.CreateUser(ctx, "sub-a", "a@example.com", "A", true)
	if err != nil {
		t.Fatal(err)
	}
	ub, err := root.CreateUser(ctx, "sub-b", "b@example.com", "B", true)
	if err != nil {
		t.Fatal(err)
	}
	return root.ForUser(ua.ID), root.ForUser(ub.ID)
}

func seedIso(t *testing.T, cx *Cortex, tag string) (e, e2 *Entity, m, m2 *Memory, ch *Chunk) {
	t.Helper()
	ctx := context.Background()
	e = &Entity{Type: "person", Name: "Alice"}
	e2 = &Entity{Type: "organization", Name: "Acme"}
	for _, x := range []*Entity{e, e2} {
		if err := cx.PutEntity(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	if err := cx.PutRelationship(ctx, &Relationship{SourceID: e.ID, TargetID: e2.ID, Type: "works_at"}); err != nil {
		t.Fatal(err)
	}
	m = &Memory{Content: "Alice enjoys hiking " + tag, Source: "s", EntityIDs: []string{e.ID}}
	m2 = &Memory{Content: "Alice enjoys hiking trails " + tag, Source: "s", EntityIDs: []string{e.ID}}
	for _, x := range []*Memory{m, m2} {
		if err := cx.PutMemory(ctx, x); err != nil {
			t.Fatal(err)
		}
		vecs, _ := cx.cfg.Embedder.Embed(ctx, []string{x.Content})
		if err := cx.putEmbedding(ctx, x.ID, "memory", vecs[0]); err != nil {
			t.Fatal(err)
		}
	}
	if err := cx.PutMemoryEdge(ctx, &MemoryEdge{SourceID: m2.ID, TargetID: m.ID, Type: EdgeExtends}); err != nil {
		t.Fatal(err)
	}
	ch = &Chunk{Content: "Alice hiking notes " + tag, EntityID: e.ID}
	if err := cx.PutChunk(ctx, ch); err != nil {
		t.Fatal(err)
	}
	vecs, _ := cx.cfg.Embedder.Embed(ctx, []string{ch.Content})
	if err := cx.putEmbedding(ctx, ch.ID, "chunk", vecs[0]); err != nil {
		t.Fatal(err)
	}
	return
}

func newIsoWorld(t *testing.T) *isoWorld {
	t.Helper()
	w := &isoWorld{}
	w.a, w.b = twoUsers(t)
	w.aEnt, w.aEnt2, w.aMem, w.aMem2, w.aChunk = seedIso(t, w.a, "alpha")
	w.bEnt, w.bEnt2, w.bMem, w.bMem2, w.bChunk = seedIso(t, w.b, "beta")
	return w
}

// snapshot summarises every row of user u so tests can prove nothing moved.
func snapshot(t *testing.T, cx *Cortex, userID string) string {
	t.Helper()
	ctx := context.Background()
	var sb strings.Builder
	for _, q := range []string{
		`SELECT id||'|'||name||'|'||type||'|'||COALESCE(deleted_at::text,'') FROM entities WHERE user_id = ? ORDER BY id`,
		`SELECT id||'|'||source_id||'|'||target_id||'|'||type||'|'||COALESCE(deleted_at::text,'') FROM relationships WHERE user_id = ? ORDER BY id`,
		`SELECT id||'|'||content||'|'||confidence::text||'|'||COALESCE(expired_at::text,'')||'|'||COALESCE(invalid_at::text,'')||'|'||COALESCE(deleted_at::text,'') FROM memories WHERE user_id = ? ORDER BY id`,
		`SELECT memory_id||'|'||entity_id FROM memory_entities WHERE user_id = ? ORDER BY 1`,
		`SELECT id||'|'||source_id||'|'||target_id||'|'||type FROM memory_edges WHERE user_id = ? ORDER BY id`,
		`SELECT id||'|'||COALESCE(entity_id,'')||'|'||content FROM chunks WHERE user_id = ? ORDER BY id`,
		`SELECT ref_type||'|'||ref_id FROM embeddings WHERE user_id = ? ORDER BY 1`,
	} {
		rows, err := cx.db.QueryContext(ctx, q, userID)
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			sb.WriteString(s + "\n")
		}
		rows.Close()
	}
	return sb.String()
}

func TestIsolationForeignIDs(t *testing.T) {
	ctx := context.Background()
	w := newIsoWorld(t)
	absent := "01ZZZZZZZZZZZZZZZZZZZZZZZZ"
	bBatch, _, err := w.b.SoftDeleteMemories(ctx, DeleteFilter{IDs: []string{w.bMem2.ID}}, "user")
	if err != nil || bBatch == "" {
		t.Fatalf("seed b batch: %q %v", bBatch, err)
	}
	aOwn := func(f func(c *Cortex, id string) error) func(id string) error {
		return func(id string) error { return f(w.a, id) }
	}

	// Each case takes an ID (b's, then an absent one) and performs the call as a.
	cases := []struct {
		name string
		kind string // "entity" or "memory": which of b's IDs to use
		call func(id string) error
	}{
		{"GetEntity", "entity", aOwn(func(c *Cortex, id string) error { _, err := c.GetEntity(ctx, id); return err })},
		{"getMemoryByID", "memory", aOwn(func(c *Cortex, id string) error { _, err := c.getMemoryByID(ctx, id); return err })},
		{"Forget", "entity", aOwn(func(c *Cortex, id string) error { return c.Forget(ctx, Filter{EntityID: id}) })},
		{"MergeKeepForeign", "entity", aOwn(func(c *Cortex, id string) error { _, err := c.MergeEntities(ctx, id, w.aEnt2.ID); return err })},
		{"MergeDropForeign", "entity", aOwn(func(c *Cortex, id string) error { _, err := c.MergeEntities(ctx, w.aEnt.ID, id); return err })},
		{"MergeDryRunKeepForeign", "entity", aOwn(func(c *Cortex, id string) error { _, err := c.MergeEntitiesDryRun(ctx, id, w.aEnt2.ID); return err })},
		{"MergeDryRunDropForeign", "entity", aOwn(func(c *Cortex, id string) error { _, err := c.MergeEntitiesDryRun(ctx, w.aEnt.ID, id); return err })},
		{"InvalidateMemory", "memory", aOwn(func(c *Cortex, id string) error { return c.InvalidateMemory(ctx, id, nil) })},
		{"GetMemoriesByEntity", "entity", aOwn(func(c *Cortex, id string) error { _, err := c.GetMemoriesByEntity(ctx, id); return err })},
		{"GetRelationships", "entity", aOwn(func(c *Cortex, id string) error { _, err := c.GetRelationships(ctx, id); return err })},
		{"GetMemoryEdges", "memory", aOwn(func(c *Cortex, id string) error { _, err := c.GetMemoryEdges(ctx, id); return err })},
		{"GetMemoryEdgesByType", "memory", aOwn(func(c *Cortex, id string) error {
			_, err := c.GetMemoryEdgesByType(ctx, id, EdgeExtends)
			return err
		})},
		{"PutMemoryEntityRef", "entity", aOwn(func(c *Cortex, id string) error {
			return c.PutMemory(ctx, &Memory{Content: "x", EntityIDs: []string{id}})
		})},
		{"PutChunkEntityRef", "entity", aOwn(func(c *Cortex, id string) error {
			return c.PutChunk(ctx, &Chunk{Content: "x", EntityID: id})
		})},
		{"PutRelationshipSource", "entity", aOwn(func(c *Cortex, id string) error {
			return c.PutRelationship(ctx, &Relationship{SourceID: id, TargetID: w.aEnt.ID, Type: "knows"})
		})},
		{"PutRelationshipTarget", "entity", aOwn(func(c *Cortex, id string) error {
			return c.PutRelationship(ctx, &Relationship{SourceID: w.aEnt.ID, TargetID: id, Type: "knows"})
		})},
		{"PutMemoryEdgeSource", "memory", aOwn(func(c *Cortex, id string) error {
			return c.PutMemoryEdge(ctx, &MemoryEdge{SourceID: id, TargetID: w.aMem.ID, Type: EdgeDerives})
		})},
		{"PutMemoryEdgeTarget", "memory", aOwn(func(c *Cortex, id string) error {
			return c.PutMemoryEdge(ctx, &MemoryEdge{SourceID: w.aMem.ID, TargetID: id, Type: EdgeDerives})
		})},
		{"Traverse", "entity", aOwn(func(c *Cortex, id string) error { _, err := c.Traverse(ctx, id, WithDepth(2)); return err })},
		{"Profile", "entity", aOwn(func(c *Cortex, id string) error { _, err := c.Profile(ctx, id); return err })},
		{"SoftDeleteEntity", "entity", aOwn(func(c *Cortex, id string) error { _, err := c.SoftDeleteEntity(ctx, id, "user"); return err })},
		{"RestoreBatch", "batch", aOwn(func(c *Cortex, id string) error { return c.RestoreBatch(ctx, id) })},
		{"TrackProfile", "entity", aOwn(func(c *Cortex, id string) error { return c.TrackProfile(ctx, id) })},
	}
	beforeB := snapshot(t, w.b, w.b.userID)
	beforeA := snapshot(t, w.a, w.a.userID)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			foreign := w.bEnt.ID
			if tc.kind == "memory" {
				foreign = w.bMem.ID
			}
			if tc.kind == "batch" {
				foreign = bBatch
			}
			errForeign := tc.call(foreign)
			errAbsent := tc.call(absent)
			if !errors.Is(errForeign, ErrNotFound) {
				t.Fatalf("foreign: err = %v, want ErrNotFound", errForeign)
			}
			if !errors.Is(errAbsent, ErrNotFound) {
				t.Fatalf("absent: err = %v, want ErrNotFound", errAbsent)
			}
			if errForeign.Error() != errAbsent.Error() {
				t.Fatalf("error text differs: foreign %q vs absent %q", errForeign, errAbsent)
			}
		})
	}
	if got := snapshot(t, w.b, w.b.userID); got != beforeB {
		t.Errorf("user b's rows changed:\n%s\n---\n%s", beforeB, got)
	}
	if got := snapshot(t, w.a, w.a.userID); got != beforeA {
		t.Errorf("user a's rows changed:\n%s\n---\n%s", beforeA, got)
	}
}

func TestIsolationEntities(t *testing.T) {
	ctx := context.Background()
	w := newIsoWorld(t)
	got, err := w.a.FindEntities(ctx, EntityFilter{NameLike: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != w.aEnt.ID {
		t.Fatalf("a sees %+v, want only its own Alice", got)
	}
	e, err := w.a.GetEntity(ctx, w.aEnt.ID)
	if err != nil || e.ID != w.aEnt.ID {
		t.Fatalf("own GetEntity: %v", err)
	}
	mems, err := w.a.GetMemoriesByEntity(ctx, w.aEnt.ID)
	if err != nil || len(mems) != 2 {
		t.Fatalf("own memories = %d err=%v", len(mems), err)
	}
	for _, m := range mems {
		if strings.Contains(m.Content, "beta") {
			t.Errorf("leaked %q", m.Content)
		}
	}
}

func TestIsolationRecall(t *testing.T) {
	ctx := context.Background()
	w := newIsoWorld(t)
	for _, c := range []*Cortex{w.a, w.b} {
		res, err := c.Recall(ctx, "Alice enjoys hiking", WithLimit(20))
		if err != nil {
			t.Fatal(err)
		}
		if len(res) == 0 {
			t.Fatal("recall returned nothing for own data")
		}
		other := "beta"
		if c == w.b {
			other = "alpha"
		}
		for _, r := range res {
			if strings.Contains(r.Content, other) {
				t.Errorf("user %s recall leaked %q", c.userID, r.Content)
			}
		}
	}
}

func TestIsolationSearchVector(t *testing.T) {
	ctx := context.Background()
	w := newIsoWorld(t)
	hits, err := w.a.SearchVector(ctx, "Alice hiking notes", 50)
	if err != nil {
		t.Fatalf("SearchVector: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("no vector hits for own data")
	}
	for _, h := range hits {
		if h.ID == w.bChunk.ID {
			t.Errorf("leaked b's chunk %s", h.ID)
		}
	}
	// Keyword searches too.
	ks, err := w.a.SearchKeyword(ctx, "Alice hiking notes", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range ks {
		if k.ID == w.bChunk.ID {
			t.Error("SearchKeyword leaked b's chunk")
		}
	}
	ms, err := w.a.SearchMemories(ctx, "Alice hiking", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.ID == w.bMem.ID || m.ID == w.bMem2.ID {
			t.Error("SearchMemories leaked b's memory")
		}
	}
}

func TestIsolationTraverse(t *testing.T) {
	ctx := context.Background()
	w := newIsoWorld(t)
	g, err := w.a.Traverse(ctx, w.aEnt.ID, WithDepth(3))
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Entities) != 2 {
		t.Fatalf("graph = %d entities, want 2", len(g.Entities))
	}
	for _, r := range g.Relationships {
		if r.ID != "" && (r.SourceID == w.bEnt.ID || r.TargetID == w.bEnt.ID || r.SourceID == w.bEnt2.ID || r.TargetID == w.bEnt2.ID) {
			t.Errorf("leaked b's relationship %s", r.ID)
		}
	}
	for _, e := range g.Entities {
		if e.ID == w.bEnt.ID || e.ID == w.bEnt2.ID {
			t.Errorf("leaked b's entity %s", e.ID)
		}
	}
}

func TestIsolationProfile(t *testing.T) {
	ctx := context.Background()
	w := newIsoWorld(t)
	p, err := w.a.Profile(ctx, w.aEnt.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range append(append([]string{}, p.Static...), p.Dynamic...) {
		if strings.Contains(line, "beta") {
			t.Errorf("profile leaked %q", line)
		}
	}
}

func TestIsolationLintMaintain(t *testing.T) {
	ctx := context.Background()
	w := newIsoWorld(t)
	past := time.Now().Add(-time.Hour)
	// A's memory is due to expire; b's identical memory must stay untouched.
	if _, err := w.a.db.ExecContext(ctx, `UPDATE memories SET forget_after = ? WHERE user_id = ?`, past, w.a.userID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.b.db.ExecContext(ctx, `UPDATE memories SET forget_after = ? WHERE user_id = ?`, past, w.b.userID); err != nil {
		t.Fatal(err)
	}
	beforeB := snapshot(t, w.b, w.b.userID)
	if _, err := w.a.Lint(ctx); err != nil {
		t.Fatalf("Lint: %v", err)
	}
	if _, err := w.a.Maintain(ctx); err != nil {
		t.Fatalf("Maintain: %v", err)
	}
	if _, err := w.a.BuildMemoryEdges(ctx); err != nil {
		t.Logf("BuildMemoryEdges: %v", err)
	}
	if got := snapshot(t, w.b, w.b.userID); got != beforeB {
		t.Errorf("a's maintenance changed b's rows:\n%s\n---\n%s", beforeB, got)
	}
	var n int
	if err := w.b.db.QueryRowContext(ctx, `SELECT count(*) FROM memories WHERE user_id = ? AND expired_at IS NOT NULL`, w.b.userID).Scan(&n); err != nil || n != 0 {
		t.Errorf("b has %d expired memories (err=%v), want 0", n, err)
	}
	// Sanity: maintenance did act on a.
	if err := w.a.db.QueryRowContext(ctx, `SELECT count(*) FROM memories WHERE user_id = ? AND expired_at IS NOT NULL`, w.a.userID).Scan(&n); err != nil || n == 0 {
		t.Errorf("a has %d expired memories (err=%v), expected maintenance to expire some", n, err)
	}
}

func TestIsolationSoftDeleteAndPurge(t *testing.T) {
	ctx := context.Background()
	w := newIsoWorld(t)
	if _, err := w.b.db.ExecContext(ctx,
		`UPDATE memories SET deleted_at = now() - interval '40 days' WHERE user_id = ? AND id = ?`, w.b.userID, w.bMem2.ID); err != nil {
		t.Fatal(err)
	}
	beforeB := snapshot(t, w.b, w.b.userID)
	if _, n, err := w.a.SoftDeleteMemories(ctx, DeleteFilter{All: true}, "user"); err != nil || n != 2 {
		t.Fatalf("a deleted %d (err %v), want its own 2", n, err)
	}
	if _, err := w.a.PurgeDeleted(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if got := snapshot(t, w.b, w.b.userID); got != beforeB {
		t.Errorf("a's delete/purge changed b's rows:\n%s\n---\n%s", beforeB, got)
	}
	var n int
	if err := w.b.db.QueryRowContext(ctx, `SELECT count(*) FROM memories WHERE user_id = ?`, w.b.userID).Scan(&n); err != nil || n != 2 {
		t.Fatalf("b memories = %d (err %v), want 2 incl. its expired row", n, err)
	}
	// b's own purge does remove its expired row.
	if got, err := w.b.PurgeDeleted(ctx, 30*24*time.Hour); err != nil || got != 1 {
		t.Fatalf("b purge = %d err=%v, want 1", got, err)
	}
}
