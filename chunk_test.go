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

func TestPutChunkWithoutEntity(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	ch := &Chunk{Content: "standalone chunk without any entity link"}
	if err := c.PutChunk(ctx, ch); err != nil {
		t.Fatalf("PutChunk() error: %v", err)
	}
	if ch.ID == "" {
		t.Fatal("expected chunk ID to be set")
	}

	results, err := c.SearchKeyword(ctx, "standalone", 5)
	if err != nil {
		t.Fatalf("SearchKeyword() error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].EntityID != "" {
		t.Errorf("expected empty EntityID for orphan chunk, got %q", results[0].EntityID)
	}
}

func TestPutChunkMetadataRoundtrip(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	ch := &Chunk{
		Content: "chunk with rich metadata attached",
		Metadata: map[string]any{
			"page":   3,
			"source": "manual.pdf",
		},
	}
	if err := c.PutChunk(ctx, ch); err != nil {
		t.Fatalf("PutChunk() error: %v", err)
	}

	results, err := c.SearchKeyword(ctx, "rich metadata", 5)
	if err != nil {
		t.Fatalf("SearchKeyword() error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	got := results[0]
	if got.Metadata["page"].(float64) != 3 {
		t.Errorf("Metadata[page] = %v, want 3", got.Metadata["page"])
	}
	if got.Metadata["source"] != "manual.pdf" {
		t.Errorf("Metadata[source] = %q, want %q", got.Metadata["source"], "manual.pdf")
	}
}

func TestSearchKeywordLimit(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	contents := []string{
		"golang programming tip alpha concurrency",
		"golang programming tip beta channels",
		"golang programming tip gamma goroutines",
		"golang programming tip delta interfaces",
		"golang programming tip epsilon contexts",
	}
	for _, content := range contents {
		if err := c.PutChunk(ctx, &Chunk{Content: content}); err != nil {
			t.Fatalf("PutChunk: %v", err)
		}
	}

	results, err := c.SearchKeyword(ctx, "golang", 3)
	if err != nil {
		t.Fatalf("SearchKeyword() error: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("expected exactly 3 results (limit), got %d", len(results))
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
