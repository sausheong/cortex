package cortex

import (
	"context"
	"testing"
)

func TestStoreAndSearchEmbedding(t *testing.T) { forEachMode(t, testStoreAndSearchEmbedding) }

func testStoreAndSearchEmbedding(t *testing.T) {
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
	vecs, err := c.h.Cfg.Embedder.Embed(ctx, []string{ch1.Content, ch2.Content})
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
	if results[0].RefID != ch1.ID {
		t.Errorf("expected top result to be ch1 (%s), got %s", ch1.ID, results[0].RefID)
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

func TestSearchMemoryVector(t *testing.T) { forEachMode(t, testSearchMemoryVector) }

func testSearchMemoryVector(t *testing.T) {
	c := openTestDBWithEmbedder(t)
	ctx := context.Background()

	// PutMemory does not embed; Remember does. Embed explicitly via the
	// same path Remember uses by storing a memory then its embedding.
	m := &Memory{Content: "Alice loves hiking in the Alps"}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}
	vecs, err := c.h.Cfg.Embedder.Embed(ctx, []string{m.Content})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if err := c.putEmbedding(ctx, m.ID, "memory", vecs[0]); err != nil {
		t.Fatalf("putEmbedding: %v", err)
	}

	got, err := c.SearchMemoryVector(ctx, "Alice loves hiking in the Alps", 10)
	if err != nil {
		t.Fatalf("SearchMemoryVector: %v", err)
	}
	if len(got) != 1 || got[0].ID != m.ID {
		t.Fatalf("expected to find memory %s, got %+v", m.ID, got)
	}
}

func TestSearchMemoryVector_HidesInvalidated(t *testing.T) {
	forEachMode(t, testSearchMemoryVector_HidesInvalidated)
}

func testSearchMemoryVector_HidesInvalidated(t *testing.T) {
	c := openTestDBWithEmbedder(t)
	ctx := context.Background()

	m := &Memory{Content: "Alice's budget is 5000"}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	vecs, _ := c.h.Cfg.Embedder.Embed(ctx, []string{m.Content})
	if err := c.putEmbedding(ctx, m.ID, "memory", vecs[0]); err != nil {
		t.Fatal(err)
	}

	// Visible before invalidation.
	got, err := c.SearchMemoryVector(ctx, "Alice's budget is 5000", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 before invalidation, got %d", len(got))
	}

	if err := c.InvalidateMemory(ctx, m.ID, nil); err != nil {
		t.Fatal(err)
	}

	got, err = c.SearchMemoryVector(ctx, "Alice's budget is 5000", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 after invalidation, got %d", len(got))
	}
}

func TestEmbeddingByRef(t *testing.T) { forEachMode(t, testEmbeddingByRef) }

func testEmbeddingByRef(t *testing.T) {
	c := openTestDBWithEmbedder(t)
	ctx := context.Background()

	m := &Memory{Content: "vectorized memory"}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	vecs, _ := c.h.Cfg.Embedder.Embed(ctx, []string{m.Content})
	if err := c.putEmbedding(ctx, m.ID, "memory", vecs[0]); err != nil {
		t.Fatal(err)
	}

	got := c.embeddingByRef(ctx, m.ID, "memory")
	if len(got) != c.h.Cfg.Embedder.Dimensions() {
		t.Fatalf("expected %d-dim vector, got %d", c.h.Cfg.Embedder.Dimensions(), len(got))
	}

	// Missing ref → nil.
	if v := c.embeddingByRef(ctx, "nonexistent", "memory"); v != nil {
		t.Fatalf("expected nil for missing ref, got %v", v)
	}
}
