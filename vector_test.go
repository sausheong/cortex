package cortex

import (
	"context"
	"strings"
	"testing"

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

func TestCheckPgvectorVersion(t *testing.T) {
	for _, v := range []string{"0.8.0", "0.8.1", "1.0.0"} {
		if err := checkPgvectorVersion(v); err != nil {
			t.Errorf("%s: unexpected error %v", v, err)
		}
	}
	err := checkPgvectorVersion("0.7.4")
	if err == nil || !strings.Contains(err.Error(), "0.8.0") {
		t.Errorf("0.7.4: want error mentioning 0.8.0, got %v", err)
	}
}

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
	if err := c.db.QueryRowContext(ctx, `SELECT dims FROM embeddings WHERE ref_id = ?`, ch.ID).Scan(&dims); err != nil || dims != 8 {
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
