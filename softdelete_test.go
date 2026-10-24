package cortex

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sausheong/cortex/internal/pgtest"
)

func openSD(t *testing.T) *Cortex {
	t.Helper()
	c, err := Open(pgtest.DSN(t), WithEmbedder(&testEmbedder{}))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return modeHandle(t, c)
}

func sdCount(t *testing.T, c *Cortex, q string, args ...any) int {
	t.Helper()
	var n int
	if err := c.h.DB.QueryRowContext(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func sdEmbed(t *testing.T, c *Cortex, id, typ, text string) {
	t.Helper()
	vecs, _ := c.h.Cfg.Embedder.Embed(context.Background(), []string{text})
	if err := c.putEmbedding(context.Background(), id, typ, vecs[0]); err != nil {
		t.Fatal(err)
	}
}

func TestSoftDeleteHidesEverywhere(t *testing.T) { forEachMode(t, testSoftDeleteHidesEverywhere) }

func testSoftDeleteHidesEverywhere(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"memories", "entities", "relationships", "chunks"} {
		t.Run(kind, func(t *testing.T) {
			c := openSD(t)
			e := &Entity{Type: "person", Name: "Alice"}
			e2 := &Entity{Type: "organization", Name: "Acme"}
			for _, x := range []*Entity{e, e2} {
				if err := c.PutEntity(ctx, x); err != nil {
					t.Fatal(err)
				}
			}
			rel := &Relationship{SourceID: e.ID, TargetID: e2.ID, Type: "works_at"}
			if err := c.PutRelationship(ctx, rel); err != nil {
				t.Fatal(err)
			}
			m := &Memory{Content: "Alice enjoys hiking", Source: "s", EntityIDs: []string{e.ID}}
			if err := c.PutMemory(ctx, m); err != nil {
				t.Fatal(err)
			}
			sdEmbed(t, c, m.ID, "memory", m.Content)
			ch := &Chunk{Content: "Alice hiking notes", EntityID: e2.ID}
			if err := c.PutChunk(ctx, ch); err != nil {
				t.Fatal(err)
			}
			sdEmbed(t, c, ch.ID, "chunk", ch.Content)

			switch kind {
			case "memories":
				if _, n, err := c.SoftDeleteMemories(ctx, DeleteFilter{IDs: []string{m.ID}}, "user"); err != nil || n != 1 {
					t.Fatalf("n=%d err=%v", n, err)
				}
			case "entities":
				if _, err := c.SoftDeleteEntity(ctx, e2.ID, "user"); err != nil {
					t.Fatal(err)
				}
			case "relationships":
				if _, err := c.h.DB.ExecContext(ctx, `UPDATE relationships SET deleted_at = now() WHERE id = ?`, rel.ID); err != nil {
					t.Fatal(err)
				}
			case "chunks":
				if _, err := c.h.DB.ExecContext(ctx, `UPDATE chunks SET deleted_at = now() WHERE id = ?`, ch.ID); err != nil {
					t.Fatal(err)
				}
			}

			recall := func() []Result {
				res, err := c.Recall(ctx, "Alice enjoys hiking", WithLimit(20))
				if err != nil {
					t.Fatal(err)
				}
				return res
			}
			switch kind {
			case "memories":
				if ms, _ := c.SearchMemories(ctx, "Alice hiking", 10); len(ms) != 0 {
					t.Error("SearchMemories shows deleted memory")
				}
				if ms, _ := c.GetMemoriesByEntity(ctx, e.ID); len(ms) != 0 {
					t.Error("GetMemoriesByEntity shows deleted memory")
				}
				if ms, _ := c.SearchMemoryVector(ctx, m.Content, 10); len(ms) != 0 {
					t.Error("SearchMemoryVector shows deleted memory")
				}
				for _, r := range recall() {
					if r.Type == "memory" {
						t.Errorf("Recall shows deleted memory %q", r.Content)
					}
				}
				for _, r := range func() []Result {
					res, _ := c.Recall(ctx, "Alice enjoys hiking", WithLimit(20), WithAsOf(time.Now().Add(time.Second)))
					return res
				}() {
					if r.Type == "memory" {
						t.Errorf("as_of recall shows deleted memory %q", r.Content)
					}
				}
			case "entities":
				if fs, _ := c.FindEntities(ctx, EntityFilter{NameLike: "Acme"}); len(fs) != 0 {
					t.Error("FindEntities shows deleted entity")
				}
				if _, err := c.GetEntity(ctx, e2.ID); !errors.Is(err, ErrNotFound) {
					t.Errorf("GetEntity err = %v", err)
				}
				g, err := c.Traverse(ctx, e.ID, WithDepth(2))
				if err != nil {
					t.Fatal(err)
				}
				if len(g.Entities) != 1 {
					t.Errorf("Traverse reaches deleted entity: %d entities", len(g.Entities))
				}
				if hs, _ := c.SearchVector(ctx, ch.Content, 10); len(hs) != 0 {
					t.Error("SearchVector shows chunk of deleted entity")
				}
			case "relationships":
				rs, err := c.GetRelationships(ctx, e.ID)
				if err != nil || len(rs) != 0 {
					t.Errorf("GetRelationships = %d err=%v", len(rs), err)
				}
				g, _ := c.Traverse(ctx, e.ID, WithDepth(2))
				if len(g.Entities) != 1 {
					t.Error("Traverse follows deleted relationship")
				}
			case "chunks":
				if ks, _ := c.SearchKeyword(ctx, "hiking notes", 10); len(ks) != 0 {
					t.Error("SearchKeyword shows deleted chunk")
				}
				if hs, _ := c.SearchVector(ctx, ch.Content, 10); len(hs) != 0 {
					t.Error("SearchVector shows deleted chunk")
				}
				for _, r := range recall() {
					if strings.Contains(r.Content, "hiking notes") {
						t.Errorf("Recall shows deleted chunk %q", r.Content)
					}
				}
			}
		})
	}
}

func TestSoftDeleteKeepsLinkedMemoryRecallable(t *testing.T) {
	forEachMode(t, testSoftDeleteKeepsLinkedMemoryRecallable)
}

func testSoftDeleteKeepsLinkedMemoryRecallable(t *testing.T) {
	ctx := context.Background()
	c := openSD(t)
	e := &Entity{Type: "person", Name: "Alice"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	m := &Memory{Content: "Alice enjoys hiking", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	sdEmbed(t, c, m.ID, "memory", m.Content)
	if _, err := c.SoftDeleteEntity(ctx, e.ID, "user"); err != nil {
		t.Fatal(err)
	}
	res, err := c.Recall(ctx, "Alice enjoys hiking", WithLimit(10))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range res {
		if r.Content == m.Content {
			found = true
		}
	}
	if !found {
		t.Fatalf("memory linked only to a deleted entity not recalled: %+v", res)
	}
}

func TestRestoreBatchExact(t *testing.T) { forEachMode(t, testRestoreBatchExact) }

func testRestoreBatchExact(t *testing.T) {
	ctx := context.Background()
	c := openSD(t)
	a := &Entity{Type: "person", Name: "Alice"}
	b := &Entity{Type: "person", Name: "Bob"}
	for _, x := range []*Entity{a, b} {
		if err := c.PutEntity(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.PutRelationship(ctx, &Relationship{SourceID: a.ID, TargetID: b.ID, Type: "knows"}); err != nil {
		t.Fatal(err)
	}
	if err := c.PutChunk(ctx, &Chunk{Content: "about alice", EntityID: a.ID}); err != nil {
		t.Fatal(err)
	}
	m1 := &Memory{Content: "one", EntityIDs: []string{a.ID}}
	m2 := &Memory{Content: "two", EntityIDs: []string{a.ID}}
	for _, m := range []*Memory{m1, m2} {
		if err := c.PutMemory(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	b1, n, err := c.SoftDeleteMemories(ctx, DeleteFilter{IDs: []string{m1.ID}}, "user")
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	b2, n, err := c.SoftDeleteMemories(ctx, DeleteFilter{IDs: []string{m2.ID}}, "connection:x")
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	b3, err := c.SoftDeleteEntity(ctx, a.ID, "user")
	if err != nil {
		t.Fatal(err)
	}
	if bs, _ := c.ListDeleteBatches(ctx); len(bs) != 3 {
		t.Fatalf("batches = %d", len(bs))
	}
	if err := c.RestoreBatch(ctx, b3); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetEntity(ctx, a.ID); err != nil {
		t.Fatalf("Alice not restored: %v", err)
	}
	if rs, _ := c.GetRelationships(ctx, a.ID); len(rs) != 1 {
		t.Errorf("relationship not restored: %d", len(rs))
	}
	if sdCount(t, c, `SELECT count(*) FROM chunks WHERE deleted_at IS NULL`) != 1 {
		t.Error("chunk not restored")
	}
	// Other batches stay deleted.
	if got := sdCount(t, c, `SELECT count(*) FROM memories WHERE deleted_at IS NOT NULL`); got != 2 {
		t.Errorf("deleted memories = %d, want 2", got)
	}
	if err := c.RestoreBatch(ctx, b3); !errors.Is(err, ErrNotFound) {
		t.Errorf("second restore err = %v, want ErrNotFound", err)
	}
	if err := c.RestoreBatch(ctx, b1); err != nil {
		t.Fatal(err)
	}
	if dl, _ := c.ListDeleted(ctx, 10, 0); len(dl) != 1 || dl[0].ID != m2.ID {
		t.Errorf("ListDeleted = %+v", dl)
	}
	_ = b2
}

func TestRestoreCollisionMerges(t *testing.T) { forEachMode(t, testRestoreCollisionMerges) }

func testRestoreCollisionMerges(t *testing.T) {
	ctx := context.Background()
	c := openSD(t)
	old := &Entity{Type: "person", Name: "Alice"}
	if err := c.PutEntity(ctx, old); err != nil {
		t.Fatal(err)
	}
	m1 := &Memory{Content: "old memory", EntityIDs: []string{old.ID}}
	if err := c.PutMemory(ctx, m1); err != nil {
		t.Fatal(err)
	}
	batch, err := c.SoftDeleteEntity(ctx, old.ID, "user")
	if err != nil {
		t.Fatal(err)
	}
	nu := &Entity{Type: "person", Name: "Alice"}
	if err := c.PutEntity(ctx, nu); err != nil {
		t.Fatal(err)
	}
	if nu.ID == old.ID {
		t.Fatal("expected a new entity")
	}
	m2 := &Memory{Content: "new memory", EntityIDs: []string{nu.ID}}
	if err := c.PutMemory(ctx, m2); err != nil {
		t.Fatal(err)
	}
	if err := c.RestoreBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	if got, err := c.FindEntities(ctx, EntityFilter{NameLike: "Alice"}); err != nil || len(got) != 1 {
		t.Fatalf("live Alices = %d (err %v), want 1", len(got), err)
	}
	mems, err := c.GetMemoriesByEntity(ctx, nu.ID)
	if err != nil || len(mems) != 2 {
		t.Fatalf("merged Alice has %d memories (err %v), want 2", len(mems), err)
	}
}

type mergeSnap struct{ ents, links, rels, attrs string }

func snapMerge(t *testing.T, c *Cortex, ids ...string) mergeSnap {
	t.Helper()
	ctx := context.Background()
	collect := func(q string, args ...any) string {
		rows, err := c.h.DB.QueryContext(ctx, q, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		sort.Strings(out)
		return strings.Join(out, "\n")
	}
	var s mergeSnap
	s.ents = collect(`SELECT id||'|'||COALESCE(deleted_at::text,'')||'|'||attributes::text||'|'||updated_at::text FROM entities WHERE user_id = ?`, c.h.UserID)
	s.links = collect(`SELECT memory_id||'|'||entity_id FROM memory_entities WHERE user_id = ?`, c.h.UserID)
	s.rels = collect(`SELECT id||'|'||source_id||'|'||target_id||'|'||type FROM relationships WHERE user_id = ? AND deleted_at IS NULL`, c.h.UserID)
	return s
}

// Legacy only: reads or writes plaintext columns with raw SQL.
func TestMergeRestoreReversesExactly(t *testing.T) {
	ctx := context.Background()
	c := openSD(t)
	mk := func(name string) *Entity {
		e := &Entity{Type: "person", Name: name, Attributes: map[string]any{"k_" + name: name, "shared": name}}
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
		return e
	}
	a, b, x, y := mk("A"), mk("B"), mk("X"), mk("Y")
	mem := func(content string, ids ...string) *Memory {
		m := &Memory{Content: content, EntityIDs: ids}
		if err := c.PutMemory(ctx, m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	mem("shared", a.ID, b.ID) // duplicate link
	mem("b only 1", b.ID)
	mem("b only 2", b.ID)
	rel := func(s, tg *Entity, typ string) {
		if err := c.PutRelationship(ctx, &Relationship{SourceID: s.ID, TargetID: tg.ID, Type: typ}); err != nil {
			t.Fatal(err)
		}
	}
	rel(a, x, "knows") // A->X
	rel(b, x, "knows") // duplicate of A->X after merge
	rel(y, b, "likes") // retargeted
	rel(a, b, "pal")   // becomes a self-loop and is dropped
	if err := c.PutChunk(ctx, &Chunk{Content: "b chunk", EntityID: b.ID}); err != nil {
		t.Fatal(err)
	}
	sdEmbed(t, c, b.ID, "entity", "B")

	before := snapMerge(t, c)
	stats, err := c.MergeEntities(ctx, a.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.BatchID == "" {
		t.Fatal("no batch id")
	}
	after := snapMerge(t, c)
	if reflect.DeepEqual(before, after) {
		t.Fatal("merge changed nothing")
	}
	if _, err := c.GetEntity(ctx, b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("B still visible: %v", err)
	}
	if sdCount(t, c, `SELECT count(*) FROM entities WHERE id = ? AND deleted_at IS NOT NULL`, b.ID) != 1 {
		t.Fatal("B should be soft-deleted, not removed")
	}
	if err := c.RestoreBatch(ctx, stats.BatchID); err != nil {
		t.Fatal(err)
	}
	restored := snapMerge(t, c)
	if restored != before {
		t.Fatalf("restore differs from pre-merge state\nbefore:%+v\nafter:%+v", before, restored)
	}
	if sdCount(t, c, `SELECT count(*) FROM embeddings WHERE ref_id = ? AND ref_type = 'entity'`, b.ID) != 1 {
		t.Error("B's embedding not restored")
	}
	if sdCount(t, c, `SELECT count(*) FROM chunks WHERE entity_id = ?`, b.ID) != 1 {
		t.Error("B's chunk not retargeted back")
	}
}

func TestPurgeAfter30Days(t *testing.T) { forEachMode(t, testPurgeAfter30Days) }

func testPurgeAfter30Days(t *testing.T) {
	ctx := context.Background()
	c := openSD(t)
	m1 := &Memory{Content: "old"}
	m2 := &Memory{Content: "recent"}
	for _, m := range []*Memory{m1, m2} {
		if err := c.PutMemory(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if _, n, err := c.SoftDeleteMemories(ctx, DeleteFilter{All: true}, "user"); err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	set := func(id string, days int) {
		if _, err := c.h.DB.ExecContext(ctx, `UPDATE memories SET deleted_at = now() - make_interval(days => ?) WHERE id = ?`, days, id); err != nil {
			t.Fatal(err)
		}
	}
	set(m1.ID, 31)
	set(m2.ID, 29)
	n, err := c.PurgeDeleted(ctx, 30*24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("purged %d err=%v, want 1", n, err)
	}
	if sdCount(t, c, `SELECT count(*) FROM memories WHERE id = ?`, m1.ID) != 0 {
		t.Error("31-day-old row survived")
	}
	if sdCount(t, c, `SELECT count(*) FROM memories WHERE id = ?`, m2.ID) != 1 {
		t.Error("29-day-old row was purged")
	}
}

func TestPurgeRemovesEmbeddingsAndLinks(t *testing.T) {
	forEachMode(t, testPurgeRemovesEmbeddingsAndLinks)
}

func testPurgeRemovesEmbeddingsAndLinks(t *testing.T) {
	ctx := context.Background()
	c := openSD(t)
	e := &Entity{Type: "person", Name: "Alice"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	m1 := &Memory{Content: "m1", EntityIDs: []string{e.ID}}
	m2 := &Memory{Content: "m2", EntityIDs: []string{e.ID}}
	for _, m := range []*Memory{m1, m2} {
		if err := c.PutMemory(ctx, m); err != nil {
			t.Fatal(err)
		}
		sdEmbed(t, c, m.ID, "memory", m.Content)
		// A second generation row for the same memory.
		vecs, _ := c.h.Cfg.Embedder.Embed(ctx, []string{m.Content})
		if err := c.PutEmbeddingGen(ctx, m.ID, "memory", 2, vecs[0]); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.PutMemoryEdge(ctx, &MemoryEdge{SourceID: m2.ID, TargetID: m1.ID, Type: EdgeExtends}); err != nil {
		t.Fatal(err)
	}
	sdEmbed(t, c, e.ID, "entity", "Alice")
	if _, err := c.SoftDeleteEntity(ctx, e.ID, "user"); err != nil {
		t.Fatal(err)
	}
	if _, n, _ := c.SoftDeleteMemories(ctx, DeleteFilter{All: true}, "user"); n != 2 {
		t.Fatalf("deleted %d", n)
	}
	if _, err := c.h.DB.ExecContext(ctx, `UPDATE memories SET deleted_at = now() - interval '31 days'`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.h.DB.ExecContext(ctx, `UPDATE entities SET deleted_at = now() - interval '31 days'`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PurgeDeleted(ctx, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`SELECT count(*) FROM embeddings`,
		`SELECT count(*) FROM memory_entities`,
		`SELECT count(*) FROM memory_edges`,
		`SELECT count(*) FROM memories`,
		`SELECT count(*) FROM entities`,
	} {
		if n := sdCount(t, c, q); n != 0 {
			t.Errorf("%s = %d, want 0", q, n)
		}
	}
}

func TestPurgeRemovesBatches(t *testing.T) { forEachMode(t, testPurgeRemovesBatches) }

func testPurgeRemovesBatches(t *testing.T) {
	ctx := context.Background()
	c := openSD(t)
	mk := func(name string) *Entity {
		e := &Entity{Type: "person", Name: name}
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
		if err := c.PutMemory(ctx, &Memory{Content: name, EntityIDs: []string{e.ID}}); err != nil {
			t.Fatal(err)
		}
		return e
	}
	a, b, p, q := mk("A"), mk("B"), mk("P"), mk("Q")
	oldMerge, err := c.MergeEntities(ctx, a.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := c.MergeEntities(ctx, p.ID, q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RestoreBatch(ctx, restored.BatchID); err != nil {
		t.Fatal(err)
	}
	c2 := &Entity{Type: "person", Name: "C"}
	d := &Entity{Type: "person", Name: "D"}
	for _, e := range []*Entity{c2, d} {
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	young, err := c.MergeEntities(ctx, c2.ID, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.h.DB.ExecContext(ctx,
		`UPDATE delete_batches SET created_at = now() - interval '31 days' WHERE id = ?`, oldMerge.BatchID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.h.DB.ExecContext(ctx,
		`UPDATE entities SET deleted_at = now() - interval '31 days' WHERE delete_batch_id = ?`, oldMerge.BatchID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PurgeDeleted(ctx, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{oldMerge.BatchID, restored.BatchID} {
		if n := sdCount(t, c, `SELECT count(*) FROM delete_batches WHERE id = ?`, id); n != 0 {
			t.Errorf("batch %s survives purge", id)
		}
		if n := sdCount(t, c, `SELECT count(*) FROM delete_batch_changes WHERE batch_id = ?`, id); n != 0 {
			t.Errorf("batch %s changes survive purge", id)
		}
	}
	bs, _ := c.ListDeleteBatches(ctx)
	if len(bs) != 1 || bs[0].ID != young.BatchID {
		t.Errorf("ListDeleteBatches = %+v, want only the young batch", bs)
	}
	if sdCount(t, c, `SELECT count(*) FROM delete_batch_changes WHERE batch_id = ?`, young.BatchID) == 0 {
		t.Error("young batch lost its changes")
	}
}

func TestSoftDeleteByConnection(t *testing.T) { forEachMode(t, testSoftDeleteByConnection) }

func testSoftDeleteByConnection(t *testing.T) {
	ctx := context.Background()
	c := openSD(t)
	e := &Entity{Type: "person", Name: "Alice"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	m := &Memory{Content: "mine"}
	other := &Memory{Content: "other"}
	for _, x := range []*Memory{m, other} {
		if err := c.PutMemory(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.h.DB.ExecContext(ctx, `UPDATE memories SET written_by_connection_id = 'c1' WHERE id = ?`, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.h.DB.ExecContext(ctx, `UPDATE entities SET written_by_connection_id = 'c1' WHERE id = ?`, e.ID); err != nil {
		t.Fatal(err)
	}
	batch, n, err := c.SoftDeleteByConnection(ctx, "c1")
	if err != nil || n != 2 || batch == "" {
		t.Fatalf("batch=%q n=%d err=%v", batch, n, err)
	}
	if sdCount(t, c, `SELECT count(*) FROM memories WHERE deleted_at IS NULL`) != 1 {
		t.Error("other connection's memory deleted")
	}
	if err := c.RestoreBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	if sdCount(t, c, fmt.Sprintf(`SELECT count(*) FROM memories WHERE deleted_at IS NULL`)) != 2 {
		t.Error("restore failed")
	}
}

func TestVectorSearchSkipsSoftDeletedBeforeLimit(t *testing.T) {
	forEachMode(t, testVectorSearchSkipsSoftDeletedBeforeLimit)
}

func testVectorSearchSkipsSoftDeletedBeforeLimit(t *testing.T) {
	ctx := context.Background()
	c := openSD(t)
	var dead []string
	for _, txt := range []string{"alpha hiking trip", "alpha hiking trail", "alpha hiking boots"} {
		m := &Memory{Content: txt, Source: "s"}
		if err := c.PutMemory(ctx, m); err != nil {
			t.Fatal(err)
		}
		sdEmbed(t, c, m.ID, "memory", m.Content)
		dead = append(dead, m.ID)
	}
	live := &Memory{Content: "completely unrelated zzz qqq", Source: "s"}
	if err := c.PutMemory(ctx, live); err != nil {
		t.Fatal(err)
	}
	sdEmbed(t, c, live.ID, "memory", live.Content)
	if _, n, err := c.SoftDeleteMemories(ctx, DeleteFilter{IDs: dead}, "user"); err != nil || n != 3 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	got, err := c.SearchMemoryVector(ctx, "alpha hiking trip", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != live.ID {
		t.Fatalf("got %v, want the live memory despite 3 nearer deleted ones", got)
	}
}

func TestSoftDeleteChunksByID(t *testing.T) { forEachMode(t, testSoftDeleteChunksByID) }

func testSoftDeleteChunksByID(t *testing.T) {
	ctx := context.Background()
	c := openSD(t)
	ch := &Chunk{Content: "zebra crossing notes"}
	if err := c.PutChunk(ctx, ch); err != nil {
		t.Fatal(err)
	}
	sdEmbed(t, c, ch.ID, "chunk", ch.Content)
	if _, _, err := c.SoftDeleteChunks(ctx, nil, "user"); err == nil {
		t.Fatal("empty ids must error")
	}
	_, n, err := c.SoftDeleteChunks(ctx, []string{ch.ID}, "user")
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	out, err := c.RecallWithStrength(ctx, "zebra crossing")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range out.Results {
		if r.ID == ch.ID {
			t.Fatalf("deleted chunk still recalled")
		}
	}
}

func TestRecallResultsCarryIDs(t *testing.T) { forEachMode(t, testRecallResultsCarryIDs) }

func testRecallResultsCarryIDs(t *testing.T) {
	ctx := context.Background()
	c := openSD(t)
	m := &Memory{Content: "Bob collects vintage zebra stamps", Source: "s"}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	sdEmbed(t, c, m.ID, "memory", m.Content)
	out, err := c.RecallWithStrength(ctx, "zebra stamps")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range out.Results {
		if r.Type == "memory" && r.ID == m.ID {
			return
		}
	}
	t.Fatalf("memory id %s not in results %+v", m.ID, out.Results)
}
