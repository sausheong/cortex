package cortex

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sausheong/cortex/internal/pgtest"
)

type dimsEmbedder struct{ n int }

func (e *dimsEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = make([]float32, e.n)
		out[i][0] = 1
	}
	return out, nil
}
func (e *dimsEmbedder) Dimensions() int { return e.n }

func TestSearchVectorUsesRowDims(t *testing.T) {
	ctx := context.Background()
	c, err := Open(pgtest.DSN(t), WithEmbedder(&dimsEmbedder{8}))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ch := &Chunk{Content: "hello vectors"}
	if err := c.PutChunk(ctx, ch); err != nil {
		t.Fatal(err)
	}
	vec := make([]float32, 8)
	vec[0] = 1
	if err := c.putEmbedding(ctx, ch.ID, "chunk", vec); err != nil {
		t.Fatal(err)
	}
	// A row of a different dimension must not break or match the search.
	other := &Chunk{Content: "sixteen dims"}
	if err := c.PutChunk(ctx, other); err != nil {
		t.Fatal(err)
	}
	vec16 := make([]float32, 16)
	vec16[0] = 1
	if err := c.putEmbedding(ctx, other.ID, "chunk", vec16); err != nil {
		t.Fatal(err)
	}
	got, err := c.SearchVector(ctx, "anything", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != ch.ID {
		t.Fatalf("SearchVector = %+v, want only %s", got, ch.ID)
	}
	var dims int
	if err := c.h.DB.QueryRowContext(ctx, `SELECT dims FROM embeddings WHERE ref_id = ?`, ch.ID).Scan(&dims); err != nil || dims != 8 {
		t.Fatalf("dims = %d err=%v, want 8", dims, err)
	}
}

func TestOpenAcceptsAnyEmbedderDims(t *testing.T) {
	dsn := pgtest.DSN(t)
	for _, n := range []int{8, 16, 512} {
		c, err := Open(dsn, WithEmbedder(&dimsEmbedder{n}))
		if err != nil {
			t.Fatalf("Open with %d dims: %v", n, err)
		}
		c.Close()
	}
}

func TestPutEmbeddingBounds(t *testing.T) {
	c, err := Open(pgtest.DSN(t), WithEmbedder(&dimsEmbedder{8}))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.putEmbedding(context.Background(), "x", "chunk", nil); err == nil {
		t.Fatal("want error for empty embedding")
	}
}

func newUserHandle(t *testing.T, root *Cortex, email string, dims int) *Cortex {
	t.Helper()
	u, err := root.CreateUser(context.Background(), "sub-"+email, email, email, true)
	if err != nil {
		t.Fatal(err)
	}
	return root.ForUser(u.ID, WithEmbedder(&dimsEmbedder{dims}))
}

func unitVec(n int) []float32 {
	v := make([]float32, n)
	v[0] = 1
	return v
}

func indexExists(t *testing.T, c *Cortex, name string) bool {
	t.Helper()
	var n int
	if err := c.h.DB.QueryRowContext(context.Background(),
		`SELECT count(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ?`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func TestVectorTwoUsersDifferentDims(t *testing.T) {
	ctx := context.Background()
	root, err := Open(pgtest.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	a := newUserHandle(t, root, "a@x.test", 8)
	b := newUserHandle(t, root, "b@x.test", 16)
	ca, cb := &Chunk{Content: "a chunk"}, &Chunk{Content: "b chunk"}
	if err := a.PutChunk(ctx, ca); err != nil {
		t.Fatal(err)
	}
	if err := b.PutChunk(ctx, cb); err != nil {
		t.Fatal(err)
	}
	if err := a.putEmbedding(ctx, ca.ID, "chunk", unitVec(8)); err != nil {
		t.Fatal(err)
	}
	if err := b.putEmbedding(ctx, cb.ID, "chunk", unitVec(16)); err != nil {
		t.Fatal(err)
	}
	ga, err := a.SearchVector(ctx, "q", 5)
	if err != nil || len(ga) != 1 || ga[0].ID != ca.ID {
		t.Fatalf("a: %+v err=%v", ga, err)
	}
	gb, err := b.SearchVector(ctx, "q", 5)
	if err != nil || len(gb) != 1 || gb[0].ID != cb.ID {
		t.Fatalf("b: %+v err=%v", gb, err)
	}
	for _, d := range []int{8, 16} {
		if err := root.EnsureVectorIndex(ctx, d, "memory"); err != nil {
			t.Fatal(err)
		}
	}
	if !indexExists(t, root, "emb_hnsw_memory_8") || !indexExists(t, root, "emb_hnsw_memory_16") {
		t.Fatal("indexes missing")
	}
}

func TestEnsureVectorIndexRejectsBadDims(t *testing.T) {
	ctx := context.Background()
	c, err := Open(pgtest.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, d := range []int{0, -1, 4001} {
		if err := c.EnsureVectorIndex(ctx, d, "memory"); err == nil {
			t.Fatalf("dims %d: want error", d)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.EnsureVectorIndex(ctx, 24, "chunk"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var n int
	if err := c.h.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = 'emb_hnsw_chunk_24'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if indexExists(t, c, "emb_hnsw_memory_0") || indexExists(t, c, "emb_hnsw_memory_4001") {
		t.Fatal("bad index created")
	}
}

func TestVectorGenerationIsolation(t *testing.T) {
	ctx := context.Background()
	c, err := Open(pgtest.DSN(t), WithEmbedder(&dimsEmbedder{8}))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	g1, g2 := &Chunk{Content: "gen one"}, &Chunk{Content: "gen two"}
	for _, ch := range []*Chunk{g1, g2} {
		if err := c.PutChunk(ctx, ch); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.PutEmbeddingGen(ctx, g1.ID, "chunk", 1, unitVec(8)); err != nil {
		t.Fatal(err)
	}
	if err := c.PutEmbeddingGen(ctx, g2.ID, "chunk", 2, unitVec(8)); err != nil {
		t.Fatal(err)
	}
	got, err := c.SearchVector(ctx, "q", 5)
	if err != nil || len(got) != 1 || got[0].ID != g1.ID {
		t.Fatalf("gen1: %+v err=%v", got, err)
	}
	if err := c.SetActiveGeneration(ctx, 2); err != nil {
		t.Fatal(err)
	}
	got, err = c.SearchVector(ctx, "q", 5)
	if err != nil || len(got) != 1 || got[0].ID != g2.ID {
		t.Fatalf("gen2: %+v err=%v", got, err)
	}
}

func countEmb(t *testing.T, c *Cortex, gen int) int {
	t.Helper()
	var n int
	if err := c.h.DB.QueryRowContext(context.Background(),
		`SELECT count(*) FROM embeddings WHERE user_id = ? AND generation = ?`, c.h.UserID, gen).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestStaleGenerationWriteRejected(t *testing.T) {
	ctx := context.Background()
	c, err := Open(pgtest.DSN(t), WithEmbedder(&dimsEmbedder{8}))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	pinned := c.WithGeneration(1)
	if err := pinned.putEmbedding(ctx, "ok", "chunk", unitVec(8)); err != nil {
		t.Fatal(err)
	}
	if err := c.SetActiveGeneration(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if err := pinned.putEmbedding(ctx, "late", "chunk", unitVec(8)); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("err = %v, want ErrStaleGeneration", err)
	}
	var n int
	if err := c.h.DB.QueryRowContext(ctx, `SELECT count(*) FROM embeddings WHERE ref_id = 'late'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("late rows = %d err=%v", n, err)
	}
	if err := c.PutEmbeddingGen(ctx, "x", "chunk", 5, unitVec(8)); err == nil {
		t.Fatal("gen 5 should be rejected")
	}
}

func TestFlipGenerationAtomic(t *testing.T) {
	ctx := context.Background()
	c, err := Open(pgtest.DSN(t), WithEmbedder(&dimsEmbedder{8}))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.putEmbedding(ctx, "m1", "memory", unitVec(8)); err != nil {
		t.Fatal(err)
	}
	if err := c.PutEmbeddingGen(ctx, "m1", "memory", 2, unitVec(8)); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	err = c.FlipGeneration(ctx, 2, func(ctx context.Context, tx *sql.Tx, put func(string, string, []float32) error) error {
		return put("tail", "memory", unitVec(8))
	}, func(tx *sql.Tx) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if g, _ := c.ActiveGeneration(ctx); g != 1 || countEmb(t, c, 1) != 1 || countEmb(t, c, 2) != 1 {
		t.Fatalf("not rolled back: g=%d g1=%d g2=%d", g, countEmb(t, c, 1), countEmb(t, c, 2))
	}
	err = c.FlipGeneration(ctx, 2, func(ctx context.Context, tx *sql.Tx, put func(string, string, []float32) error) error {
		return put("tail", "memory", unitVec(8))
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if g, _ := c.ActiveGeneration(ctx); g != 2 || countEmb(t, c, 1) != 0 || countEmb(t, c, 2) != 2 {
		t.Fatalf("flip: g=%d g1=%d g2=%d", g, countEmb(t, c, 1), countEmb(t, c, 2))
	}
}

func TestFlipDoesNotSelfDeadlock(t *testing.T) {
	c, err := Open(pgtest.DSN(t), WithEmbedder(&dimsEmbedder{8}))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = c.FlipGeneration(ctx, 2, func(ctx context.Context, tx *sql.Tx, put func(string, string, []float32) error) error {
		for i := 0; i < 3; i++ {
			if err := put(fmt.Sprintf("r%d", i), "memory", unitVec(8)); err != nil {
				return err
			}
		}
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestVectorWriteTakesSharedLock(t *testing.T) {
	c, err := Open(pgtest.DSN(t), WithEmbedder(&dimsEmbedder{8}))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	bg := context.Background()
	tx, err := c.h.DB.BeginTx(bg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(bg, `SELECT pg_advisory_xact_lock(`+reembedLockSQL+`)`, c.h.UserID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(bg, 500*time.Millisecond)
	defer cancel()
	if err := c.PutEmbeddingGen(ctx, "blocked", "chunk", 1, unitVec(8)); err == nil {
		t.Fatal("write should block while exclusive lock held")
	}
	tx.Rollback()
	if err := c.PutEmbeddingGen(bg, "free", "chunk", 1, unitVec(8)); err != nil {
		t.Fatal(err)
	}
}
