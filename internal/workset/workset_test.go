package workset_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sausheong/cortex/internal/enctest"
	"github.com/sausheong/cortex/internal/graph"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/memory"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/workset"
)

var bg = context.Background()

func setup(t *testing.T) {
	t.Helper()
	workset.Reset()
	t.Cleanup(workset.Reset)
}

// openEnc opens a fully encrypted user. CreateUser still writes a plaintext
// owner entity (encrypted by the Task 16 enrollment), which the row-form
// guard rightly rejects, so the test removes it.
func openEnc(t *testing.T) *enctest.Env {
	t.Helper()
	env := enctest.Open(t, "encrypted")
	env.Exec(`DELETE FROM entities WHERE user_id = $1 AND enc IS NOT TRUE`, env.UserID)
	return env
}

func seed(t *testing.T, env *enctest.Env) (ent string, mem string) {
	t.Helper()
	g := graph.New(env.H)
	m := memory.New(env.H)
	a := &model.Entity{Type: "person", Name: "Ada Lovelace", Attributes: map[string]any{"k": "v"}}
	b := &model.Entity{Type: "place", Name: "London"}
	for _, e := range []*model.Entity{a, b} {
		if err := g.PutEntity(bg, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.PutRelationship(bg, &model.Relationship{SourceID: a.ID, TargetID: b.ID, Type: "lives_in"}); err != nil {
		t.Fatal(err)
	}
	if err := g.AddAlias(bg, a.ID, "handle", "@ada"); err != nil {
		t.Fatal(err)
	}
	mm := &model.Memory{Content: "Ada wrote the first program", Source: "notes", EntityIDs: []string{a.ID}}
	if err := m.PutMemory(bg, mm); err != nil {
		t.Fatal(err)
	}
	return a.ID, mm.ID
}

func TestWorksetLoadsDecryptedEntitiesMemoriesRels(t *testing.T) {
	setup(t)
	env := openEnc(t)
	ent, mem := seed(t, env)
	s, err := workset.For(bg, env.H)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Entities()) != 2 || len(s.Relationships()) != 1 || len(s.Aliases()) < 1 {
		t.Fatalf("ents=%d rels=%d aliases=%d", len(s.Entities()), len(s.Relationships()), len(s.Aliases()))
	}
	e, ok := s.EntityByID(ent)
	if !ok || e.Name != "Ada Lovelace" || e.Attributes["k"] != "v" {
		t.Fatalf("entity %+v", e)
	}
	m, ok := s.MemoryByID(mem)
	if !ok || m.Content != "Ada wrote the first program" || len(m.EntityIDs) != 1 || m.EntityIDs[0] != ent {
		t.Fatalf("memory %+v", m)
	}
	if got := s.RelationshipsFiltered(false, nil, time.Now()); len(got) != 1 || got[0].Type != "lives_in" {
		t.Fatalf("rels %+v", got)
	}
	hits := s.MemIndex().Search("first program", 5, nil)
	if len(hits) != 1 || hits[0].ID != mem {
		t.Fatalf("memindex %v", hits)
	}
}

func TestWorksetMergesLegacyAndEncryptedRows(t *testing.T) {
	setup(t)
	env := enctest.Open(t, "migrating")
	legacy := env.HandleFor(env.UserID, "none", nil)
	pm := &model.Memory{Content: "legacy plaintext memory", Source: "s"}
	if err := memory.New(legacy).PutMemory(bg, pm); err != nil {
		t.Fatal(err)
	}
	if err := graph.New(legacy).PutEntity(bg, &model.Entity{Type: "person", Name: "Legacy Lee"}); err != nil {
		t.Fatal(err)
	}
	em := &model.Memory{Content: "encrypted memory", Source: "s"}
	if err := memory.New(env.H).PutMemory(bg, em); err != nil {
		t.Fatal(err)
	}
	if err := graph.New(env.H).PutEntity(bg, &model.Entity{Type: "person", Name: "Enc Eve"}); err != nil {
		t.Fatal(err)
	}
	if env.Count(`SELECT count(*) FROM memories WHERE enc`) != 1 || env.Count(`SELECT count(*) FROM memories WHERE NOT enc`) != 1 {
		t.Fatal("expected one legacy and one encrypted memory")
	}
	s, err := workset.For(bg, env.H)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, m := range s.Memories() {
		seen[m.Content]++
	}
	if seen["legacy plaintext memory"] != 1 || seen["encrypted memory"] != 1 || len(seen) != 2 {
		t.Fatalf("memories %v", seen)
	}
	names := map[string]int{}
	for _, e := range s.Entities() {
		names[e.Name]++
	}
	if names["Legacy Lee"] != 1 || names["Enc Eve"] != 1 {
		t.Fatalf("entities %v", names)
	}
}

func TestWorksetExcludesSoftDeleted(t *testing.T) {
	setup(t)
	env := openEnc(t)
	ent, mem := seed(t, env)
	ch := &model.Chunk{Content: "body", Metadata: map[string]any{"source": "doc"}}
	if err := memory.New(env.H).PutChunk(bg, ch); err != nil {
		t.Fatal(err)
	}
	env.Exec(`UPDATE memories SET deleted_at = now() WHERE id = $1`, mem)
	env.Exec(`UPDATE chunks SET deleted_at = now() WHERE id = $1`, ch.ID)
	env.Exec(`UPDATE relationships SET deleted_at = now()`)
	env.Exec(`UPDATE entities SET deleted_at = now() WHERE id = $1`, ent)
	workset.Invalidate(env.UserID)
	s, err := workset.For(bg, env.H)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Memories()) != 0 || len(s.ChunkHeaders()) != 0 || len(s.Relationships()) != 0 || len(s.Entities()) != 1 {
		t.Fatalf("m=%d c=%d r=%d e=%d", len(s.Memories()), len(s.ChunkHeaders()), len(s.Relationships()), len(s.Entities()))
	}
	for _, a := range s.Aliases() {
		if a.EntityID == ent {
			t.Fatal("alias of deleted entity loaded")
		}
	}
}

func TestWorksetHonoursValidity(t *testing.T) {
	setup(t)
	env := openEnc(t)
	ent, mem := seed(t, env)
	past := time.Now().Add(-48 * time.Hour)
	mid := time.Now().Add(-24 * time.Hour)
	env.Exec(`UPDATE memories SET invalid_at = $1 WHERE id = $2`, mid, mem)
	env.Exec(`UPDATE relationships SET invalid_at = $1`, mid)
	_ = ent
	workset.Invalidate(env.UserID)
	s, err := workset.For(bg, env.H)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Memories()) != 1 {
		t.Fatal("Memories() must keep non-deleted rows regardless of validity")
	}
	if got := s.MemoriesMode(model.TemporalMode{}); len(got) != 0 {
		t.Fatalf("current mode returned %d", len(got))
	}
	if got := s.MemoriesMode(model.TemporalMode{IncludeInvalid: true}); len(got) != 1 {
		t.Fatalf("include invalid returned %d", len(got))
	}
	// As of a time before invalid_at but after creation.
	after := time.Now().Add(-time.Hour)
	_ = after
	asOf := mid.Add(-time.Minute)
	// created_at is now(), so as-of before creation excludes it.
	if got := s.MemoriesMode(model.TemporalMode{AsOf: &asOf}); len(got) != 0 {
		t.Fatalf("as-of before creation returned %d", len(got))
	}
	_ = past
	now := time.Now()
	if got := s.RelationshipsFiltered(false, nil, now); len(got) != 0 {
		t.Fatalf("closed edge in current view: %d", len(got))
	}
	if got := s.RelationshipsFiltered(true, nil, now); len(got) != 1 {
		t.Fatalf("closed edge missing with includeInvalid: %d", len(got))
	}
	future := now.Add(time.Hour)
	// created before "future", invalid_at (mid) before it: not valid then.
	if got := s.RelationshipsFiltered(false, &future, now); len(got) != 0 {
		t.Fatalf("as-of after close: %d", len(got))
	}
}

func TestWorksetInvalidatedOnWrite(t *testing.T) {
	setup(t)
	env := openEnc(t)
	seed(t, env)
	s, err := workset.For(bg, env.H)
	if err != nil {
		t.Fatal(err)
	}
	n := len(s.Memories())
	if err := memory.New(env.H).PutMemory(bg, &model.Memory{Content: "brand new", Source: "s"}); err != nil {
		t.Fatal(err)
	}
	s2, _ := workset.For(bg, env.H)
	if len(s2.Memories()) != n+1 {
		t.Fatalf("write not visible: %d vs %d", len(s2.Memories()), n+1)
	}
	if len(s.Memories()) != n {
		t.Fatal("an earlier snapshot must not change")
	}
	if err := graph.New(env.H).PutEntity(bg, &model.Entity{Type: "person", Name: "New Person"}); err != nil {
		t.Fatal(err)
	}
	s3, _ := workset.For(bg, env.H)
	if len(s3.Entities()) != 3 {
		t.Fatalf("entity write not visible: %d", len(s3.Entities()))
	}
	// Only the touched slice reloads.
	st0, _ := workset.StatsFor(env.UserID)
	if err := memory.New(env.H).PutMemory(bg, &model.Memory{Content: "another", Source: "s"}); err != nil {
		t.Fatal(err)
	}
	if _, err := workset.For(bg, env.H); err != nil {
		t.Fatal(err)
	}
	st1, _ := workset.StatsFor(env.UserID)
	if st1.Loads != st0.Loads+1 {
		t.Fatalf("loads %d -> %d, want one slice reload", st0.Loads, st1.Loads)
	}
}

func TestWorksetEvictsAfter10MinIdle(t *testing.T) {
	setup(t)
	env := openEnc(t)
	seed(t, env)
	now := time.Now()
	workset.SetClock(func() time.Time { return now })
	if _, err := workset.For(bg, env.H); err != nil {
		t.Fatal(err)
	}
	now = now.Add(9 * time.Minute)
	workset.Sweep()
	if !workset.Cached(env.UserID) {
		t.Fatal("evicted too early")
	}
	workset.Touch(env.UserID)
	now = now.Add(9 * time.Minute)
	workset.Sweep()
	if !workset.Cached(env.UserID) {
		t.Fatal("Touch must extend life")
	}
	now = now.Add(11 * time.Minute)
	workset.Sweep()
	if workset.Cached(env.UserID) {
		t.Fatal("not evicted after 10 idle minutes")
	}
	if _, err := workset.For(bg, env.H); err != nil {
		t.Fatal(err)
	}
	if !workset.Cached(env.UserID) {
		t.Fatal("reload after eviction failed")
	}
}

func TestWorksetEvictsLRUUnderCap(t *testing.T) {
	setup(t)
	env := openEnc(t)
	seed(t, env)
	u2 := env.NewUser("second", "encrypted")
	env.Exec(`DELETE FROM entities WHERE user_id = $1 AND enc IS NOT TRUE`, u2)
	h2 := env.HandleFor(u2, "encrypted", env.UK)
	if err := memory.New(h2).PutMemory(bg, &model.Memory{Content: "second user memory", Source: "s"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	workset.SetClock(func() time.Time { return now })
	s1, err := workset.For(bg, env.H)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	workset.SetCap(s1.Bytes() + 10)
	if _, err := workset.For(bg, h2); err != nil {
		t.Fatal(err)
	}
	if workset.Cached(env.UserID) {
		t.Fatal("least recently used user not evicted")
	}
	if !workset.Cached(u2) {
		t.Fatal("the loading user must stay cached")
	}
}

func TestWorksetKeylessIsLocked(t *testing.T) {
	setup(t)
	env := openEnc(t)
	if _, err := workset.For(bg, env.Keyless); !errors.Is(err, model.ErrLocked) {
		t.Fatalf("keyless: %v", err)
	}
	plain := env.HandleFor(env.UserID, "none", nil)
	if _, err := workset.For(bg, plain); !errors.Is(err, model.ErrLocked) {
		t.Fatalf("unencrypted: %v", err)
	}
}

func TestSetNeverServedToDifferentKey(t *testing.T) {
	setup(t)
	env := openEnc(t)
	seed(t, env)
	if _, err := workset.For(bg, env.H); err != nil {
		t.Fatal(err)
	}
	st0, _ := workset.StatsFor(env.UserID)
	other, err := keys.NewUK()
	if err != nil {
		t.Fatal(err)
	}
	defer other.Zero()
	hw := env.HandleFor(env.UserID, "encrypted", other)
	if _, err := workset.For(bg, hw); !errors.Is(err, model.ErrLocked) {
		t.Fatalf("different key: %v", err)
	}
	st1, ok := workset.StatsFor(env.UserID)
	if !ok || st1.Loads != st0.Loads {
		t.Fatalf("a wrong key must not reload or evict: %+v %+v", st0, st1)
	}
	if _, err := workset.For(bg, env.H); err != nil {
		t.Fatalf("right key still served: %v", err)
	}
}

func TestDropOnLogoutDoesNotBreakMCPUser(t *testing.T) {
	setup(t)
	env := openEnc(t)
	ent, _ := seed(t, env)
	if _, err := workset.For(bg, env.H); err != nil {
		t.Fatal(err)
	}
	workset.Drop(env.UserID)
	if workset.Cached(env.UserID) {
		t.Fatal("Drop must evict")
	}
	if env.Count(`SELECT count(*) FROM entities WHERE id = $1`, ent) != 1 {
		t.Fatal("Drop must not touch the database")
	}
	s, err := workset.For(bg, env.H)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.EntityByID(ent); !ok {
		t.Fatal("reload after Drop lost data")
	}
}

func TestRecallAccessUpdateDoesNotInvalidateCache(t *testing.T) {
	setup(t)
	env := openEnc(t)
	_, mem := seed(t, env)
	s, err := workset.For(bg, env.H)
	if err != nil {
		t.Fatal(err)
	}
	st0, _ := workset.StatsFor(env.UserID)
	at := time.Now()
	workset.RecordAccess(env.UserID, []string{mem}, at)
	workset.RecordAccess(env.UserID, []string{mem}, at)
	s2, err := workset.For(bg, env.H)
	if err != nil {
		t.Fatal(err)
	}
	st1, _ := workset.StatsFor(env.UserID)
	if st1.Loads != st0.Loads {
		t.Fatalf("access update reloaded: %d -> %d", st0.Loads, st1.Loads)
	}
	for _, x := range []*workset.Set{s, s2} {
		n, last, ok := x.MemoryAccess(mem)
		if !ok || n != 2 || last == nil || !last.Equal(at) {
			t.Fatalf("access = %d %v %v", n, last, ok)
		}
	}
}

func TestVectorBruteForceMatchesSQLCosine(t *testing.T) {
	setup(t)
	env := enctest.Open(t, "migrating")
	legacy := env.HandleFor(env.UserID, "none", nil)
	lm := memory.New(legacy)
	vecs := [][]float32{{1, 0, 0}, {0.9, 0.3, 0}, {0.5, 0.5, 0.5}, {0, 1, 0}, {0, 0.2, 1}, {-1, 0.1, 0}}
	for i, v := range vecs {
		m := &model.Memory{Content: "m" + string(rune('a'+i)), Source: "s"}
		if err := lm.PutMemory(bg, m); err != nil {
			t.Fatal(err)
		}
		if err := lm.PutEmbedding(bg, m.ID, "memory", v); err != nil {
			t.Fatal(err)
		}
	}
	q := []float32{1, 0.2, 0.1}
	want, err := lm.SearchVectorRaw(bg, q, "memory", 4)
	if err != nil {
		t.Fatal(err)
	}
	s, err := workset.For(bg, env.H)
	if err != nil {
		t.Fatal(err)
	}
	gen, _ := lm.ActiveGeneration(bg)
	got, err := s.SearchVectors(bg, q, "memory", gen, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("len %d vs %d", len(got), len(want))
	}
	for i := range want {
		if got[i].RefID != want[i].RefID {
			t.Fatalf("rank %d: %s vs %s", i, got[i].RefID, want[i].RefID)
		}
		if d := got[i].Similarity - want[i].Similarity; d > 0.01 || d < -0.01 {
			t.Fatalf("sim %d: %v vs %v", i, got[i].Similarity, want[i].Similarity)
		}
	}
}

func TestVectorFiltersByGenerationAndDims(t *testing.T) {
	setup(t)
	env := openEnc(t)
	m := memory.New(env.H)
	mk := func(c string) string {
		x := &model.Memory{Content: c, Source: "s"}
		if err := m.PutMemory(bg, x); err != nil {
			t.Fatal(err)
		}
		return x.ID
	}
	a, b, c := mk("a"), mk("b"), mk("c")
	if err := m.PutEmbeddingGen(bg, a, "memory", 1, []float32{1, 0}); err != nil {
		t.Fatal(err)
	}
	if err := m.PutEmbeddingGen(bg, b, "memory", 2, []float32{1, 0}); err != nil {
		t.Fatal(err)
	}
	if err := m.PutEmbeddingGen(bg, c, "memory", 1, []float32{1, 0, 0}); err != nil {
		t.Fatal(err)
	}
	s, err := workset.For(bg, env.H)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.SearchVectors(bg, []float32{1, 0}, "memory", 1, 10)
	if len(got) != 1 || got[0].RefID != a {
		t.Fatalf("gen1 dims2: %+v", got)
	}
	got, _ = s.SearchVectors(bg, []float32{1, 0}, "memory", 2, 10)
	if len(got) != 1 || got[0].RefID != b {
		t.Fatalf("gen2: %+v", got)
	}
	got, _ = s.SearchVectors(bg, []float32{1, 0, 0}, "memory", 1, 10)
	if len(got) != 1 || got[0].RefID != c {
		t.Fatalf("dims3: %+v", got)
	}
	// A soft-deleted memory's vector is skipped.
	env.Exec(`UPDATE memories SET deleted_at = now() WHERE id = $1`, a)
	workset.Invalidate(env.UserID, workset.KindMemories)
	s, _ = workset.For(bg, env.H)
	if got, _ := s.SearchVectors(bg, []float32{1, 0}, "memory", 1, 10); len(got) != 0 {
		t.Fatalf("deleted ref surfaced: %+v", got)
	}
}

func TestChunkBodyLazyDecrypt(t *testing.T) {
	setup(t)
	env := openEnc(t)
	ch := &model.Chunk{Content: "the lazy body", Metadata: map[string]any{"source": "Doc"}}
	if err := memory.New(env.H).PutChunk(bg, ch); err != nil {
		t.Fatal(err)
	}
	s, err := workset.For(bg, env.H)
	if err != nil {
		t.Fatal(err)
	}
	hs := s.ChunkHeaders()
	if len(hs) != 1 || hs[0].Source != "Doc" {
		t.Fatalf("headers %+v", hs)
	}
	if st, _ := workset.StatsFor(env.UserID); st.ContentDecrypts != 0 {
		t.Fatalf("headers decrypted bodies: %d", st.ContentDecrypts)
	}
	body, err := s.ChunkBody(bg, ch.ID)
	if err != nil || body != "the lazy body" {
		t.Fatalf("%q %v", body, err)
	}
	if st, _ := workset.StatsFor(env.UserID); st.ContentDecrypts != 1 {
		t.Fatalf("decrypts %d", st.ContentDecrypts)
	}
	if _, err := s.ChunkBody(bg, "nope"); err == nil {
		t.Fatal("unknown chunk must error")
	}
}

func TestBM25IndexCountedAndEvictable(t *testing.T) {
	setup(t)
	env := openEnc(t)
	for _, body := range []string{"alpha beta gamma delta", "epsilon zeta eta theta"} {
		if err := memory.New(env.H).PutChunk(bg, &model.Chunk{Content: body, Metadata: map[string]any{"source": "d"}}); err != nil {
			t.Fatal(err)
		}
	}
	s, err := workset.For(bg, env.H)
	if err != nil {
		t.Fatal(err)
	}
	before := s.Bytes()
	if s.BM25Cached() {
		t.Fatal("BM25 must not build at load")
	}
	ix, err := s.BM25(bg)
	if err != nil {
		t.Fatal(err)
	}
	if hits := ix.Search("gamma", 5, nil); len(hits) != 1 {
		t.Fatalf("bm25 hits %v", hits)
	}
	if !s.BM25Cached() || s.Bytes() <= before {
		t.Fatalf("BM25 not counted: %d -> %d", before, s.Bytes())
	}
	// Another user's load under a tight cap evicts the BM25 index first and
	// keeps the rest of the first user's set.
	u2 := env.NewUser("other", "encrypted")
	env.Exec(`DELETE FROM entities WHERE user_id = $1 AND enc IS NOT TRUE`, u2)
	h2 := env.HandleFor(u2, "encrypted", env.UK)
	if err := memory.New(h2).PutMemory(bg, &model.Memory{Content: "x", Source: "s"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	workset.SetClock(func() time.Time { return now })
	workset.Touch(env.UserID)
	now = now.Add(time.Second)
	workset.SetCap(before + 300)
	if _, err := workset.For(bg, h2); err != nil {
		t.Fatal(err)
	}
	if !workset.Cached(env.UserID) {
		t.Fatal("BM25 eviction must be tried before evicting the user")
	}
	if s.BM25Cached() {
		t.Fatal("BM25 index not evicted")
	}
	// Rebuilt on demand.
	if _, err := s.BM25(bg); err != nil {
		t.Fatal(err)
	}
	if st, _ := workset.StatsFor(env.UserID); st.BM25Builds != 2 {
		t.Fatalf("builds %d", st.BM25Builds)
	}
}

func TestWorksetConcurrentForInvalidate(t *testing.T) {
	setup(t)
	env := openEnc(t)
	seed(t, env)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				switch (i + j) % 4 {
				case 0:
					workset.Invalidate(env.UserID, workset.KindMemories, workset.KindChunks)
				case 1:
					workset.RecordAccess(env.UserID, []string{"x"}, time.Now())
				default:
					s, err := workset.For(bg, env.H)
					if err != nil {
						t.Error(err)
						return
					}
					_ = s.Memories()
					_ = s.MemIndex()
					_, _ = s.SearchVectors(bg, []float32{1}, "memory", 1, 1)
				}
			}
		}(i)
	}
	wg.Wait()
}
