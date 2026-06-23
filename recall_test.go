package cortex

import (
	"context"
	"testing"
	"time"
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

func TestRecallWithLimit(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	// Seed 5 memories all matching "go programming".
	names := []string{"tip-alpha", "tip-beta", "tip-gamma", "tip-delta", "tip-epsilon"}
	contents := []string{
		"go programming tip: use goroutines for concurrency",
		"go programming tip: use channels for communication",
		"go programming tip: use defer for cleanup",
		"go programming tip: use interfaces for abstraction",
		"go programming tip: use context for cancellation",
	}
	for i, name := range names {
		e := &Entity{Type: "note", Name: name, Source: "test"}
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatalf("PutEntity(%s): %v", name, err)
		}
		mem := &Memory{Content: contents[i], EntityIDs: []string{e.ID}, Source: "test"}
		if err := c.PutMemory(ctx, mem); err != nil {
			t.Fatalf("PutMemory(%s): %v", name, err)
		}
	}

	c.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
			return []StructuredQuery{
				{Type: "memory_lookup", Params: map[string]any{"query": q}},
			}, nil
		},
	})

	results, err := c.Recall(ctx, "go programming", WithLimit(2))
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(results) > 2 {
		t.Errorf("expected at most 2 results (limit=2), got %d", len(results))
	}
	if len(results) == 0 {
		t.Error("expected at least 1 result, got 0")
	}
}

func TestRecallVectorSearch(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	c.SetEmbedder(&testEmbedder{})

	// Store two chunks and their embeddings.
	ch1 := &Chunk{Content: "Go concurrency with goroutines and channels"}
	ch2 := &Chunk{Content: "Python data science with pandas and numpy"}
	if err := c.PutChunk(ctx, ch1); err != nil {
		t.Fatalf("PutChunk(1): %v", err)
	}
	if err := c.PutChunk(ctx, ch2); err != nil {
		t.Fatalf("PutChunk(2): %v", err)
	}

	vecs, err := c.cfg.embedder.Embed(ctx, []string{ch1.Content, ch2.Content})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if err := c.putEmbedding(ctx, ch1.ID, "chunk", vecs[0]); err != nil {
		t.Fatalf("putEmbedding(1): %v", err)
	}
	if err := c.putEmbedding(ctx, ch2.ID, "chunk", vecs[1]); err != nil {
		t.Fatalf("putEmbedding(2): %v", err)
	}

	c.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
			return []StructuredQuery{
				{Type: "vector_search", Params: map[string]any{"query": q}},
			}, nil
		},
	})

	results, err := c.Recall(ctx, "Go goroutines concurrency", WithLimit(10))
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected vector search results, got 0")
	}

	foundChunk := false
	for _, r := range results {
		if r.Type == "chunk" {
			foundChunk = true
		}
	}
	if !foundChunk {
		t.Errorf("expected chunk results from vector search, got types: %v", resultTypes(results))
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
	extractFn         func(ctx context.Context, text, prompt string) (ExtractionResult, error)
	decomposeFn       func(ctx context.Context, query string) ([]StructuredQuery, error)
	summarizeFn       func(ctx context.Context, texts []string) (string, error)
	detectConflictsFn func(ctx context.Context, memories []Memory) ([]ConflictPair, error)
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

func (m *mockLLM) DetectConflicts(ctx context.Context, memories []Memory) ([]ConflictPair, error) {
	if m.detectConflictsFn != nil {
		return m.detectConflictsFn(ctx, memories)
	}
	return nil, nil
}

func TestMockLLM_SatisfiesReconciler(t *testing.T) {
	var r Reconciler = &mockLLM{
		detectConflictsFn: func(_ context.Context, mems []Memory) ([]ConflictPair, error) {
			return []ConflictPair{{StaleID: "x", SupersededByID: "y", Reason: "test"}}, nil
		},
	}
	got, err := r.DetectConflicts(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SupersededByID != "y" {
		t.Fatalf("unexpected detection result: %+v", got)
	}
}

func TestRecall_ResultIncludesConfidence(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Type: "person", Name: "Alice Recall", Confidence: 0.6}
	if err := cx.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	m := &Memory{Content: "alice recall test memory", EntityIDs: []string{e.ID}, Confidence: 0.4}
	if err := cx.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}

	results, err := cx.Recall(ctx, "alice recall")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("no results")
	}
	// At least one result should have non-default confidence (0.4 or 0.6).
	found := false
	for _, r := range results {
		if r.Confidence == 0.4 || r.Confidence == 0.6 {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no result with expected confidence values; got %+v", results)
	}
}

func TestRecall_WithMinConfidence_FiltersBelow(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Type: "person", Name: "FilterAlice"}
	if err := cx.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	low := &Memory{Content: "filteralice rumor", EntityIDs: []string{e.ID}, Confidence: 0.2}
	high := &Memory{Content: "filteralice fact", EntityIDs: []string{e.ID}, Confidence: 0.9}
	if err := cx.PutMemory(ctx, low); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutMemory(ctx, high); err != nil {
		t.Fatal(err)
	}

	// Without filter: both should appear.
	all, err := cx.Recall(ctx, "filteralice")
	if err != nil {
		t.Fatal(err)
	}
	hasLow := false
	for _, r := range all {
		if r.Confidence == 0.2 {
			hasLow = true
		}
	}
	if !hasLow {
		t.Error("expected low-confidence result without filter")
	}

	// With filter at 0.5: only high should appear.
	filtered, err := cx.Recall(ctx, "filteralice", WithMinConfidence(0.5))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range filtered {
		if r.Confidence < 0.5 {
			t.Errorf("WithMinConfidence(0.5) returned r.Confidence=%v", r.Confidence)
		}
	}
}

func TestRecall_UsesMemoryVector(t *testing.T) {
	c := openTestDBWithEmbedder(t)
	ctx := context.Background()

	m := &Memory{Content: "Quarterly board meeting is in Zurich"}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}
	vecs, _ := c.cfg.embedder.Embed(ctx, []string{m.Content})
	if err := c.putEmbedding(ctx, m.ID, "memory", vecs[0]); err != nil {
		t.Fatalf("putEmbedding: %v", err)
	}

	// Decompose into memory_vector only, to isolate the new path.
	c.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
			return []StructuredQuery{
				{Type: "memory_vector", Params: map[string]any{"query": q}},
			}, nil
		},
	})

	results, err := c.Recall(ctx, "Quarterly board meeting is in Zurich", WithLimit(10))
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(results) == 0 || results[0].Content != m.Content {
		t.Fatalf("expected memory via vector path, got %+v", results)
	}
}

func TestRecall_ConfidenceWeightsRanking(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	// Two memories matching the same query at similar FTS rank, different
	// confidence. The higher-confidence one should rank first after weighting.
	low := &Memory{Content: "Project Phoenix ships in March", Confidence: 0.3}
	high := &Memory{Content: "Project Phoenix ships in April", Confidence: 0.95}
	if err := c.PutMemory(ctx, low); err != nil {
		t.Fatal(err)
	}
	if err := c.PutMemory(ctx, high); err != nil {
		t.Fatal(err)
	}

	c.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
			return []StructuredQuery{
				{Type: "memory_lookup", Params: map[string]any{"query": q}},
			}, nil
		},
	})

	results, err := c.Recall(ctx, "Project Phoenix ships", WithLimit(10))
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Content != high.Content {
		t.Fatalf("expected high-confidence memory first, got %q", results[0].Content)
	}
}

func TestRecall_ZeroConfidenceRanksLast(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	// Two memories matching the same query. One has a genuine 0.0 confidence
	// (the least-trustworthy state — a negative input clamps to 0.0 at ingest),
	// the other 0.5. The 0.0 one must NOT be rescued to the top; the 0.5 one
	// must rank above it.
	zero := &Memory{Content: "Project Hydra ships in May", Confidence: -0.1}
	mid := &Memory{Content: "Project Hydra ships in June", Confidence: 0.5}
	if err := c.PutMemory(ctx, zero); err != nil {
		t.Fatal(err)
	}
	if err := c.PutMemory(ctx, mid); err != nil {
		t.Fatal(err)
	}

	c.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
			return []StructuredQuery{
				{Type: "memory_lookup", Params: map[string]any{"query": q}},
			}, nil
		},
	})

	results, err := c.Recall(ctx, "Project Hydra ships", WithLimit(10))
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Content != mid.Content {
		t.Fatalf("expected 0.5-confidence memory first (0.0 must not be rescued to top), got %q", results[0].Content)
	}
}

func TestRecall_ReinjectsSourceChunk(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	const src = "meeting-notes-2026-06"
	// A memory and a chunk that share a source.
	if err := c.PutMemory(ctx, &Memory{
		Content: "Decision: adopt Postgres for analytics",
		Source:  src,
	}); err != nil {
		t.Fatal(err)
	}
	ch := &Chunk{
		Content:  "After debating SQLite vs Postgres, the team decided to adopt Postgres for analytics because of concurrent writers.",
		Metadata: map[string]any{"source": src},
	}
	if err := c.PutChunk(ctx, ch); err != nil {
		t.Fatal(err)
	}

	c.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
			return []StructuredQuery{
				{Type: "memory_lookup", Params: map[string]any{"query": q}},
			}, nil
		},
	})

	results, err := c.Recall(ctx, "Postgres analytics decision", WithLimit(10))
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	var mem *Result
	for i := range results {
		if results[i].Type == "memory" {
			mem = &results[i]
			break
		}
	}
	if mem == nil {
		t.Fatal("expected a memory result")
	}
	excerpt, ok := mem.Metadata["source_excerpt"].(string)
	if !ok || excerpt != ch.Content {
		t.Fatalf("expected source_excerpt to be the chunk content, got %v", mem.Metadata["source_excerpt"])
	}
}

func TestRecallWithStrength_AbstainsWhenEmpty(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	c.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
			return []StructuredQuery{
				{Type: "memory_lookup", Params: map[string]any{"query": q}},
			}, nil
		},
	})

	// Nothing stored → no results → abstain.
	out, err := c.RecallWithStrength(ctx, "anything at all")
	if err != nil {
		t.Fatalf("RecallWithStrength: %v", err)
	}
	if !out.Abstain {
		t.Fatalf("expected Abstain=true on empty recall, got %+v", out)
	}
	if out.Strength != 0 {
		t.Fatalf("expected Strength 0 on empty recall, got %v", out.Strength)
	}
}

func TestRecallWithStrength_DoesNotAbstainWhenConfident(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	if err := c.PutMemory(ctx, &Memory{Content: "User's name is Sau Sheong", Confidence: 1.0}); err != nil {
		t.Fatal(err)
	}
	c.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
			return []StructuredQuery{
				{Type: "memory_lookup", Params: map[string]any{"query": q}},
			}, nil
		},
	})

	out, err := c.RecallWithStrength(ctx, "What is the user's name")
	if err != nil {
		t.Fatalf("RecallWithStrength: %v", err)
	}
	if out.Abstain {
		t.Fatalf("expected Abstain=false with a confident hit, got %+v", out)
	}
	if out.Strength <= 0 {
		t.Fatalf("expected positive strength, got %v", out.Strength)
	}
}

func TestRecall_WithIncludeInvalid(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	stale := &Memory{Content: "Alice's budget is 5000"}
	if err := c.PutMemory(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if err := c.InvalidateMemory(ctx, stale.ID, nil); err != nil {
		t.Fatal(err)
	}

	c.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
			return []StructuredQuery{{Type: "memory_lookup", Params: map[string]any{"query": q}}}, nil
		},
	})

	// Default recall: invalidated memory hidden.
	def, err := c.Recall(ctx, "budget", WithLimit(10))
	if err != nil {
		t.Fatal(err)
	}
	if len(def) != 0 {
		t.Fatalf("default recall should hide invalidated memory, got %d", len(def))
	}

	// WithIncludeInvalid: it reappears.
	all, err := c.Recall(ctx, "budget", WithLimit(10), WithIncludeInvalid())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("WithIncludeInvalid should return the invalidated memory, got %d", len(all))
	}
}

func TestRecall_WithAsOf(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	m := &Memory{Content: "Alice's budget is 5000"}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	// Backdate created_at so the as-of window is meaningful.
	ingest := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := c.db.ExecContext(ctx, `UPDATE memories SET created_at = ? WHERE id = ?`, ingest, m.ID); err != nil {
		t.Fatal(err)
	}
	// Invalidate as of 2026-03-01 (fact stopped being true then).
	cut := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if err := c.InvalidateMemory(ctx, m.ID, &cut); err != nil {
		t.Fatal(err)
	}

	c.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
			return []StructuredQuery{{Type: "memory_lookup", Params: map[string]any{"query": q}}}, nil
		},
	})

	// As of before the cutoff: the fact was still valid → returned.
	before := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	got, err := c.Recall(ctx, "budget", WithLimit(10), WithAsOf(before))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("as-of before cutoff should return the memory, got %d", len(got))
	}

	// As of after the cutoff: invalidated → hidden.
	after := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	got, err = c.Recall(ctx, "budget", WithLimit(10), WithAsOf(after))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("as-of after cutoff should hide the memory, got %d", len(got))
	}
}

func TestRecall_IncludeInvalidBeatsAsOf(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	m := &Memory{Content: "Alice's budget is 5000"}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	// Backdate created_at so the as-of window is meaningful.
	ingest := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := c.db.ExecContext(ctx, `UPDATE memories SET created_at = ? WHERE id = ?`, ingest, m.ID); err != nil {
		t.Fatal(err)
	}
	// Invalidate as of 2026-03-01 (fact stopped being true then).
	cut := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if err := c.InvalidateMemory(ctx, m.ID, &cut); err != nil {
		t.Fatal(err)
	}

	c.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
			return []StructuredQuery{{Type: "memory_lookup", Params: map[string]any{"query": q}}}, nil
		},
	})

	// As of after the cutoff would normally hide the invalidated memory
	// (see TestRecall_WithAsOf). But WithIncludeInvalid takes precedence:
	// no filtering is applied, so the memory is returned.
	after := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	got, err := c.Recall(ctx, "budget", WithLimit(10), WithAsOf(after), WithIncludeInvalid())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("WithIncludeInvalid should override the as-of filter, got %d", len(got))
	}
}

func TestRecall_SurfacesSpeaker(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	if err := c.PutMemory(ctx, &Memory{Content: "Standups moved to 10am", Speaker: "assistant"}); err != nil {
		t.Fatal(err)
	}
	c.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
			return []StructuredQuery{{Type: "memory_lookup", Params: map[string]any{"query": q}}}, nil
		},
	})

	results, err := c.Recall(ctx, "standups", WithLimit(10))
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	var mem *Result
	for i := range results {
		if results[i].Type == "memory" {
			mem = &results[i]
			break
		}
	}
	if mem == nil || mem.Speaker != "assistant" {
		t.Fatalf("expected memory result with speaker 'assistant', got %+v", results)
	}
}

func TestAugmentForEmbedding(t *testing.T) {
	// No entities → unchanged.
	if got := augmentForEmbedding("plain content", nil); got != "plain content" {
		t.Fatalf("no-entity case: got %q", got)
	}
	// Entities → sorted prefix, content preserved.
	got := augmentForEmbedding("ships in April", []string{"Stripe", "Alice"})
	want := "Entities: Alice, Stripe. ships in April"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// Empty-string entities are ignored (no blank tokens).
	got = augmentForEmbedding("x", []string{"", "Bob", ""})
	want = "Entities: Bob. x"
	if got != want {
		t.Fatalf("blank-token case: got %q want %q", got, want)
	}
}

func TestRecall_TemporalFilterNarrowsByEventTime(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	jan := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	mar := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	if err := c.PutMemory(ctx, &Memory{Content: "budget was 5000", ValidAt: &jan}); err != nil {
		t.Fatal(err)
	}
	if err := c.PutMemory(ctx, &Memory{Content: "budget was 9000", ValidAt: &mar}); err != nil {
		t.Fatal(err)
	}

	// LLM decomposes into a temporal_filter scoped to Q1 (Jan–Mar 1).
	c.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
			return []StructuredQuery{
				{Type: "temporal_filter", Params: map[string]any{
					"query": q,
					"from":  "2026-01-01",
					"to":    "2026-03-01",
				}},
			}, nil
		},
	})

	got, err := c.Recall(ctx, "what was the budget in Q1", WithLimit(10))
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(got) != 1 || got[0].Content != "budget was 5000" {
		t.Fatalf("expected only the Q1 memory, got %+v", got)
	}
}
