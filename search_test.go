package cortex

import (
	"context"
	"math"
	"path/filepath"
	"testing"
)

func openTestDBWithEmbedder(t *testing.T) *Cortex {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	embedder := &testEmbedder{}
	c, err := Open(dbPath, WithEmbedder(embedder))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// testEmbedder produces deterministic 8-dim normalized vectors from text bytes.
type testEmbedder struct{}

func (e *testEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	result := make([][]float32, len(texts))
	for i, text := range texts {
		result[i] = testDeterministicVector(text)
	}
	return result, nil
}

func (e *testEmbedder) Dimensions() int { return 8 }

func testDeterministicVector(text string) []float32 {
	const dims = 8
	vec := make([]float32, dims)
	for i, b := range []byte(text) {
		vec[i%dims] += float32(b)
	}
	// Normalize to unit length.
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)
	if norm > 0 {
		for i := range vec {
			vec[i] = float32(float64(vec[i]) / norm)
		}
	}
	return vec
}

func TestStoreAndSearchEmbedding(t *testing.T) {
	c := openTestDBWithEmbedder(t)
	ctx := context.Background()

	// Create an entity for the chunks.
	e := &Entity{Type: "document", Name: "test.md", Source: "test"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatalf("PutEntity() error: %v", err)
	}

	// Create two chunks with known content.
	ch1 := &Chunk{EntityID: e.ID, Content: "Go is a programming language"}
	ch2 := &Chunk{EntityID: e.ID, Content: "The weather is sunny today"}

	if err := c.PutChunk(ctx, ch1); err != nil {
		t.Fatalf("PutChunk(1) error: %v", err)
	}
	if err := c.PutChunk(ctx, ch2); err != nil {
		t.Fatalf("PutChunk(2) error: %v", err)
	}

	// Generate embeddings and store them.
	vecs, err := c.cfg.embedder.Embed(ctx, []string{ch1.Content, ch2.Content})
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}

	if err := c.putEmbedding(ctx, ch1.ID, "chunk", vecs[0]); err != nil {
		t.Fatalf("putEmbedding(1) error: %v", err)
	}
	if err := c.putEmbedding(ctx, ch2.ID, "chunk", vecs[1]); err != nil {
		t.Fatalf("putEmbedding(2) error: %v", err)
	}

	// Search with a query similar to ch1 (programming-related).
	queryVec := testDeterministicVector("Go programming language")
	results, err := c.searchVectorRaw(ctx, queryVec, "chunk", 10)
	if err != nil {
		t.Fatalf("searchVectorRaw() error: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// The first result should be ch1 (closest to "Go programming language").
	if results[0].refID != ch1.ID {
		t.Errorf("expected top result to be ch1 (%s), got %s", ch1.ID, results[0].refID)
	}

	// Test the exported SearchVector method.
	chunks, err := c.SearchVector(ctx, "Go programming language", 10)
	if err != nil {
		t.Fatalf("SearchVector() error: %v", err)
	}

	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks from SearchVector, got %d", len(chunks))
	}

	if chunks[0].ID != ch1.ID {
		t.Errorf("expected top chunk to be ch1 (%s), got %s", ch1.ID, chunks[0].ID)
	}
	if chunks[0].Content != ch1.Content {
		t.Errorf("expected content %q, got %q", ch1.Content, chunks[0].Content)
	}
}
