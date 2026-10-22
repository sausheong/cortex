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

func metaDims(t *testing.T, c *Cortex) string {
	t.Helper()
	var v string
	if err := c.db.QueryRowContext(context.Background(),
		`SELECT value FROM cortex_meta WHERE key = 'embedding_dims'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

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

func TestVectorDimsFromEmbedder(t *testing.T) {
	c, err := Open(pgtest.DSN(t), WithEmbedder(&dimsEmbedder{8}))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got := metaDims(t, c); got != "8" {
		t.Fatalf("dims = %s, want 8", got)
	}
}

func TestVectorDimsDefault512(t *testing.T) {
	c, err := Open(pgtest.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got := metaDims(t, c); got != "512" {
		t.Fatalf("dims = %s, want 512", got)
	}
}

func TestVectorDimsMismatch(t *testing.T) {
	dsn := pgtest.DSN(t)
	c, err := Open(dsn, WithEmbedder(&dimsEmbedder{8}))
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	_, err = Open(dsn, WithEmbedder(&dimsEmbedder{16}))
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("want mismatch error, got %v", err)
	}
}

func TestVectorPartialIndexes(t *testing.T) {
	c, err := Open(pgtest.DSN(t), WithEmbedder(&dimsEmbedder{8}))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var n int
	if err := c.db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM pg_indexes WHERE tablename = 'embeddings' AND schemaname = current_schema() AND indexdef ILIKE '%hnsw%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("hnsw indexes = %d, want 3", n)
	}
}

func TestPutEmbeddingWrongDims(t *testing.T) {
	c, err := Open(pgtest.DSN(t), WithEmbedder(&dimsEmbedder{8}))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.putEmbedding(context.Background(), "x", "chunk", []float32{1, 2, 3})
	if err == nil || !strings.Contains(err.Error(), "want 8") {
		t.Fatalf("want dims error, got %v", err)
	}
}
