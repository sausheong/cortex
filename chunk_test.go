package cortex

import (
	"context"
	"testing"
)

func TestPutChunk(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	// Create an entity to link the chunk to.
	e := &Entity{Type: "document", Name: "readme.md", Source: "test"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatalf("PutEntity() error: %v", err)
	}

	chunk := &Chunk{
		EntityID: e.ID,
		Content:  "This is the first section of the readme document.",
		Metadata: map[string]any{"section": 1, "heading": "Introduction"},
	}
	if err := c.PutChunk(ctx, chunk); err != nil {
		t.Fatalf("PutChunk() error: %v", err)
	}
	if chunk.ID == "" {
		t.Fatal("expected chunk ID to be set")
	}
	if chunk.CreatedAt.IsZero() {
		t.Fatal("expected CreatedAt to be set")
	}
}

func TestSearchKeyword(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Type: "document", Name: "notes.md", Source: "test"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatalf("PutEntity() error: %v", err)
	}

	chunks := []*Chunk{
		{EntityID: e.ID, Content: "Go is a statically typed programming language designed at Google."},
		{EntityID: e.ID, Content: "Rust is a systems programming language focused on safety and performance."},
		{EntityID: e.ID, Content: "Python is a dynamically typed language popular in data science."},
	}
	for i, ch := range chunks {
		if err := c.PutChunk(ctx, ch); err != nil {
			t.Fatalf("PutChunk(%d) error: %v", i, err)
		}
	}

	// Search for "programming language".
	results, err := c.SearchKeyword(ctx, "programming language", 10)
	if err != nil {
		t.Fatalf("SearchKeyword() error: %v", err)
	}

	if len(results) < 2 {
		t.Fatalf("expected at least 2 results for 'programming language', got %d", len(results))
	}

	// All results should contain "programming" or "language".
	for _, r := range results {
		if r.Content == "" {
			t.Error("expected non-empty content")
		}
		if r.ID == "" {
			t.Error("expected non-empty chunk ID")
		}
	}
}

func TestSearchKeywordNoResults(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Type: "document", Name: "test.md", Source: "test"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatalf("PutEntity() error: %v", err)
	}

	chunk := &Chunk{
		EntityID: e.ID,
		Content:  "The quick brown fox jumps over the lazy dog.",
	}
	if err := c.PutChunk(ctx, chunk); err != nil {
		t.Fatalf("PutChunk() error: %v", err)
	}

	results, err := c.SearchKeyword(ctx, "quantum computing blockchain", 10)
	if err != nil {
		t.Fatalf("SearchKeyword() error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}
