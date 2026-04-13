package cortex

import (
	"context"
	"testing"
)

func TestRecallFindsMemories(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	// Seed an entity and a memory directly.
	e := &Entity{Type: "person", Name: "Alice", Source: "test"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatalf("PutEntity: %v", err)
	}

	mem := &Memory{
		Content:   "Alice works at Stripe as an engineer",
		EntityIDs: []string{e.ID},
		Source:    "test",
	}
	if err := c.PutMemory(ctx, mem); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}

	// Also store a chunk so keyword search can find it.
	ch := &Chunk{Content: "Alice works at Stripe as an engineer"}
	if err := c.PutChunk(ctx, ch); err != nil {
		t.Fatalf("PutChunk: %v", err)
	}

	// Set up LLM mock to decompose into memory_lookup + keyword_search.
	c.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, query string) ([]StructuredQuery, error) {
			return []StructuredQuery{
				{Type: "memory_lookup", Params: map[string]any{"query": query}},
				{Type: "keyword_search", Params: map[string]any{"query": query}},
			}, nil
		},
	})

	results, err := c.Recall(ctx, "Alice Stripe", WithLimit(10))
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("expected at least 1 result, got 0")
	}

	// Verify that a memory result appears.
	foundMemory := false
	for _, r := range results {
		if r.Type == "memory" {
			foundMemory = true
			if r.Content != "Alice works at Stripe as an engineer" {
				t.Errorf("unexpected memory content: %q", r.Content)
			}
		}
	}
	if !foundMemory {
		t.Errorf("expected a memory result in recall output, got types: %v", resultTypes(results))
	}
}

func TestRecallNoResults(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	results, err := c.Recall(ctx, "nonexistent query")
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results from empty DB, got %d", len(results))
	}
}

func TestRecallFallbackWithoutLLM(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	c.SetLLM(nil) // no LLM configured

	// Seed a memory.
	e := &Entity{Type: "person", Name: "Bob", Source: "test"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatalf("PutEntity: %v", err)
	}
	mem := &Memory{
		Content:   "Bob is a Go developer",
		EntityIDs: []string{e.ID},
		Source:    "test",
	}
	if err := c.PutMemory(ctx, mem); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}

	results, err := c.Recall(ctx, "Bob developer")
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("expected results from fallback decomposition, got 0")
	}
}

func TestRecallWithGraphTraverse(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	// Seed entities and a relationship.
	alice := &Entity{Type: "person", Name: "Alice", Source: "test"}
	stripe := &Entity{Type: "organization", Name: "Stripe", Source: "test"}
	if err := c.PutEntity(ctx, alice); err != nil {
		t.Fatalf("PutEntity(Alice): %v", err)
	}
	if err := c.PutEntity(ctx, stripe); err != nil {
		t.Fatalf("PutEntity(Stripe): %v", err)
	}
	rel := &Relationship{SourceID: alice.ID, TargetID: stripe.ID, Type: "works_at"}
	if err := c.PutRelationship(ctx, rel); err != nil {
		t.Fatalf("PutRelationship: %v", err)
	}

	// Decompose to graph_traverse.
	c.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, query string) ([]StructuredQuery, error) {
			return []StructuredQuery{
				{Type: "graph_traverse", Params: map[string]any{"query": "Alice"}},
			}, nil
		},
	})

	results, err := c.Recall(ctx, "Alice")
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("expected graph traverse results, got 0")
	}

	// Should find entity results.
	foundEntity := false
	for _, r := range results {
		if r.Type == "entity" {
			foundEntity = true
		}
	}
	if !foundEntity {
		t.Errorf("expected entity results from graph traverse, got types: %v", resultTypes(results))
	}
}

// helpers

func resultTypes(results []Result) []string {
	types := make([]string, len(results))
	for i, r := range results {
		types[i] = r.Type
	}
	return types
}

// mockLLM is a test-only LLM mock.
type mockLLM struct {
	extractFn   func(ctx context.Context, text, prompt string) (ExtractionResult, error)
	decomposeFn func(ctx context.Context, query string) ([]StructuredQuery, error)
	summarizeFn func(ctx context.Context, texts []string) (string, error)
}

func (m *mockLLM) Extract(ctx context.Context, text, prompt string) (ExtractionResult, error) {
	if m.extractFn != nil {
		return m.extractFn(ctx, text, prompt)
	}
	return ExtractionResult{}, nil
}

func (m *mockLLM) Decompose(ctx context.Context, query string) ([]StructuredQuery, error) {
	if m.decomposeFn != nil {
		return m.decomposeFn(ctx, query)
	}
	return nil, nil
}

func (m *mockLLM) Summarize(ctx context.Context, texts []string) (string, error) {
	if m.summarizeFn != nil {
		return m.summarizeFn(ctx, texts)
	}
	return "", nil
}
