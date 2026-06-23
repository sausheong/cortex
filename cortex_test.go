package cortex

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRemember(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	// Configure mock extractor to return known entities
	c.SetExtractor(&mockExtractor{
		extractFn: func(_ context.Context, content, _ string) (*Extraction, error) {
			return &Extraction{
				Entities: []Entity{
					{Type: "person", Name: "Alice"},
					{Type: "organization", Name: "Stripe"},
				},
				Relationships: []Relationship{
					{SourceID: "Alice", TargetID: "Stripe", Type: "works_at"},
				},
				Memories: []Memory{
					{Content: "Alice works at Stripe"},
				},
			}, nil
		},
	})

	err := c.Remember(ctx, "Alice works at Stripe as an engineer", WithSource("test"))
	if err != nil {
		t.Fatalf("Remember: %v", err)
	}

	// Verify entities
	people, _ := c.FindEntities(ctx, EntityFilter{Type: "person"})
	if len(people) != 1 || people[0].Name != "Alice" {
		t.Errorf("expected person Alice, got %v", people)
	}
	orgs, _ := c.FindEntities(ctx, EntityFilter{Type: "organization"})
	if len(orgs) != 1 || orgs[0].Name != "Stripe" {
		t.Errorf("expected org Stripe, got %v", orgs)
	}

	// Verify relationships were stored with resolved IDs
	rels, _ := c.GetRelationships(ctx, people[0].ID)
	if len(rels) != 1 || rels[0].Type != "works_at" {
		t.Errorf("expected 1 works_at relationship, got %v", rels)
	}
	if rels[0].SourceID != people[0].ID || rels[0].TargetID != orgs[0].ID {
		t.Errorf("relationship IDs not resolved: source=%s target=%s", rels[0].SourceID, rels[0].TargetID)
	}

	// Verify memory
	mems, _ := c.SearchMemories(ctx, "Stripe", 10)
	if len(mems) != 1 {
		t.Errorf("expected 1 memory, got %d", len(mems))
	}

	// Verify chunk was stored (keyword search)
	chunks, _ := c.SearchKeyword(ctx, "engineer", 10)
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk, got %d", len(chunks))
	}
}

func TestRememberNoExtractor(t *testing.T) {
	c := openTestDB(t)
	c.SetExtractor(nil)
	ctx := context.Background()

	err := c.Remember(ctx, "some content")
	if err == nil {
		t.Fatal("expected error when no extractor configured")
	}
}

func TestRememberIdempotentEntities(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	c.SetExtractor(&mockExtractor{
		extractFn: func(_ context.Context, _, _ string) (*Extraction, error) {
			return &Extraction{
				Entities: []Entity{
					{Type: "person", Name: "Alice"},
				},
			}, nil
		},
	})

	// Remember twice with the same entity
	if err := c.Remember(ctx, "first"); err != nil {
		t.Fatalf("first Remember: %v", err)
	}
	if err := c.Remember(ctx, "second"); err != nil {
		t.Fatalf("second Remember: %v", err)
	}

	// Should still be only 1 entity (upsert)
	people, _ := c.FindEntities(ctx, EntityFilter{Type: "person"})
	if len(people) != 1 {
		t.Errorf("expected 1 person after upsert, got %d", len(people))
	}
}

func TestForgetByEntityID(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	// Create entity, chunk linked to entity, memory linked to entity.
	e := &Entity{Type: "person", Name: "Alice", Source: "test"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatalf("PutEntity: %v", err)
	}

	ch := &Chunk{EntityID: e.ID, Content: "Alice is an engineer"}
	if err := c.PutChunk(ctx, ch); err != nil {
		t.Fatalf("PutChunk: %v", err)
	}

	mem := &Memory{
		Content:   "Alice works at Stripe",
		EntityIDs: []string{e.ID},
		Source:    "test",
	}
	if err := c.PutMemory(ctx, mem); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}

	// Create a relationship.
	org := &Entity{Type: "organization", Name: "Stripe", Source: "test"}
	if err := c.PutEntity(ctx, org); err != nil {
		t.Fatalf("PutEntity(org): %v", err)
	}
	rel := &Relationship{SourceID: e.ID, TargetID: org.ID, Type: "works_at"}
	if err := c.PutRelationship(ctx, rel); err != nil {
		t.Fatalf("PutRelationship: %v", err)
	}

	// Forget by entity ID.
	if err := c.Forget(ctx, Filter{EntityID: e.ID}); err != nil {
		t.Fatalf("Forget: %v", err)
	}

	// Verify entity is gone.
	_, err := c.GetEntity(ctx, e.ID)
	if err == nil {
		t.Error("expected entity to be deleted")
	}

	// Verify relationships are gone.
	rels, _ := c.GetRelationships(ctx, e.ID)
	if len(rels) != 0 {
		t.Errorf("expected 0 relationships, got %d", len(rels))
	}

	// Verify memory is gone (orphaned since its only entity link was removed).
	mems, _ := c.SearchMemories(ctx, "Stripe", 10)
	if len(mems) != 0 {
		t.Errorf("expected 0 memories, got %d", len(mems))
	}

	// Verify the other entity (Stripe) still exists.
	_, err = c.GetEntity(ctx, org.ID)
	if err != nil {
		t.Errorf("Stripe entity should still exist: %v", err)
	}
}

func TestForgetRemovesMemoryFTS(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	// Seed an entity and a memory linked to it. Forgetting the entity orphans
	// the memory, which must remove its memories_fts row too — not just the
	// memories row. (Backfill on Open only INSERTs missing FTS rows; it never
	// removes stale ones, so a reorder of the delete logic would silently
	// orphan FTS rows. This test guards the delete ordering.)
	e := &Entity{Type: "person", Name: "Ftsalice", Source: "test"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatalf("PutEntity: %v", err)
	}
	mem := &Memory{
		Content:   "Ftsalice migrated billing to Stripe",
		EntityIDs: []string{e.ID},
		Source:    "test",
	}
	if err := c.PutMemory(ctx, mem); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}

	// Query the FTS table directly (not joined to memories), so a leaked FTS
	// row is detected even after the memories row itself is deleted.
	matchesFTS := func() bool {
		var n int
		if err := c.db.QueryRowContext(ctx,
			`SELECT count(*) FROM memories_fts WHERE memories_fts MATCH ?`, "Stripe",
		).Scan(&n); err != nil {
			t.Fatalf("count memories_fts: %v", err)
		}
		return n > 0
	}

	// Before: the memory must be findable via the FTS index.
	if !matchesFTS() {
		t.Fatal("expected memory to match via memories_fts before Forget")
	}

	if err := c.Forget(ctx, Filter{EntityID: e.ID}); err != nil {
		t.Fatalf("Forget: %v", err)
	}

	// After: the FTS row must be gone (no MATCH-joinable rows remain).
	if matchesFTS() {
		t.Fatal("expected memories_fts row to be removed after Forget; FTS still matches")
	}
}

func TestForgetBySource(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	// Create 2 entities with different sources.
	e1 := &Entity{Type: "person", Name: "Alice", Source: "source-a"}
	e2 := &Entity{Type: "person", Name: "Bob", Source: "source-b"}
	if err := c.PutEntity(ctx, e1); err != nil {
		t.Fatalf("PutEntity(Alice): %v", err)
	}
	if err := c.PutEntity(ctx, e2); err != nil {
		t.Fatalf("PutEntity(Bob): %v", err)
	}

	// Create chunks for each.
	ch1 := &Chunk{EntityID: e1.ID, Content: "Alice content"}
	ch2 := &Chunk{EntityID: e2.ID, Content: "Bob content"}
	if err := c.PutChunk(ctx, ch1); err != nil {
		t.Fatalf("PutChunk(Alice): %v", err)
	}
	if err := c.PutChunk(ctx, ch2); err != nil {
		t.Fatalf("PutChunk(Bob): %v", err)
	}

	// Create memories for each.
	mem1 := &Memory{Content: "Alice memory", EntityIDs: []string{e1.ID}, Source: "source-a"}
	mem2 := &Memory{Content: "Bob memory", EntityIDs: []string{e2.ID}, Source: "source-b"}
	if err := c.PutMemory(ctx, mem1); err != nil {
		t.Fatalf("PutMemory(Alice): %v", err)
	}
	if err := c.PutMemory(ctx, mem2); err != nil {
		t.Fatalf("PutMemory(Bob): %v", err)
	}

	// Forget source-a.
	if err := c.Forget(ctx, Filter{Source: "source-a"}); err != nil {
		t.Fatalf("Forget: %v", err)
	}

	// Alice should be gone.
	_, err := c.GetEntity(ctx, e1.ID)
	if err == nil {
		t.Error("expected Alice entity to be deleted")
	}

	// Bob should remain.
	got, err := c.GetEntity(ctx, e2.ID)
	if err != nil {
		t.Fatalf("Bob entity should still exist: %v", err)
	}
	if got.Name != "Bob" {
		t.Errorf("expected Bob, got %s", got.Name)
	}

	// Alice's memory should be gone (orphaned).
	aliceMems, _ := c.SearchMemories(ctx, "Alice", 10)
	if len(aliceMems) != 0 {
		t.Errorf("expected 0 Alice memories, got %d", len(aliceMems))
	}

	// Bob's memory should remain.
	bobMems, _ := c.SearchMemories(ctx, "Bob", 10)
	if len(bobMems) != 1 {
		t.Errorf("expected 1 Bob memory, got %d", len(bobMems))
	}
}

func TestForgetRequiresFilter(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	err := c.Forget(ctx, Filter{})
	if err == nil {
		t.Fatal("expected error when no filter provided")
	}
}

func TestRememberWithEmbedder(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	c.SetEmbedder(&testEmbedder{})
	c.SetExtractor(&mockExtractor{
		extractFn: func(_ context.Context, _, _ string) (*Extraction, error) {
			return &Extraction{
				Memories: []Memory{{Content: "Alice works at Stripe"}},
			}, nil
		},
	})

	if err := c.Remember(ctx, "Alice works at Stripe as an engineer"); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	// Chunk embedding should be stored — vector search must return it.
	chunks, err := c.SearchVector(ctx, "Alice Stripe engineer", 5)
	if err != nil {
		t.Fatalf("SearchVector: %v", err)
	}
	if len(chunks) == 0 {
		t.Error("expected at least one chunk from vector search after Remember with embedder")
	}
}

func TestRememberWithMaxChunkSize(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	c.SetExtractor(&mockExtractor{})

	// Three paragraphs of ~90 chars each; at maxChunkChars=100 each becomes its own chunk.
	// Each paragraph starts with a searchable keyword followed by filler characters.
	para := "programming " + strings.Repeat("x", 78) // 90 chars
	content := para + "\n\n" + para + "\n\n" + para

	if err := c.Remember(ctx, content, WithMaxChunkChars(100)); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	results, err := c.SearchKeyword(ctx, "programming", 10)
	if err != nil {
		t.Fatalf("SearchKeyword: %v", err)
	}
	if len(results) < 2 {
		t.Errorf("expected multiple chunks from long split content, got %d", len(results))
	}
}

func TestRememberUnresolvableRelationshipSkipped(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	// Extractor returns a relationship where the target name ("Bob") is not
	// in the extracted entities list, so it cannot be resolved to an ID.
	c.SetExtractor(&mockExtractor{
		extractFn: func(_ context.Context, _, _ string) (*Extraction, error) {
			return &Extraction{
				Entities: []Entity{
					{Type: "person", Name: "Alice"},
				},
				Relationships: []Relationship{
					{SourceID: "Alice", TargetID: "Bob", Type: "knows"},
				},
			}, nil
		},
	})

	if err := c.Remember(ctx, "Alice knows Bob from work"); err != nil {
		t.Fatalf("Remember should succeed even with unresolvable relationship: %v", err)
	}

	// Alice should be stored.
	people, err := c.FindEntities(ctx, EntityFilter{Type: "person"})
	if err != nil {
		t.Fatalf("FindEntities: %v", err)
	}
	if len(people) != 1 || people[0].Name != "Alice" {
		t.Errorf("expected Alice to be stored, got %v", people)
	}

	// No relationship stored (Bob was unresolvable).
	rels, err := c.GetRelationships(ctx, people[0].ID)
	if err != nil {
		t.Fatalf("GetRelationships: %v", err)
	}
	if len(rels) != 0 {
		t.Errorf("expected 0 relationships (unresolvable skipped), got %d", len(rels))
	}
}

// mockExtractor is a test-only extractor mock.
type mockExtractor struct {
	extractFn func(ctx context.Context, content, contentType string) (*Extraction, error)
}

func (m *mockExtractor) Extract(ctx context.Context, content, contentType string) (*Extraction, error) {
	if m.extractFn != nil {
		return m.extractFn(ctx, content, contentType)
	}
	return &Extraction{}, nil
}

func TestRemember_EventTimeFlowsToAsOfRecall(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	when := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	c.SetExtractor(&mockExtractor{
		extractFn: func(_ context.Context, _, _ string) (*Extraction, error) {
			return &Extraction{
				Memories: []Memory{
					{Content: "Alice joined Stripe", ValidAt: &when},
				},
			}, nil
		},
	})
	// Force the memory_lookup path so recall reads memories (with temporal mode).
	c.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
			return []StructuredQuery{
				{Type: "memory_lookup", Params: map[string]any{"query": q}},
			}, nil
		},
	})

	if err := c.Remember(ctx, "Alice joined Stripe in March 2026"); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	// Backdate created_at so the as-of window is meaningful: validAsOfClause also
	// gates on created_at <= t (system-time), not just valid_at (event-time). See
	// TestRecall_WithAsOf. Without this, the memory ingested "now" is excluded from
	// any as-of in the past regardless of its event-time.
	ingest := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := c.db.ExecContext(ctx,
		`UPDATE memories SET created_at = ?`, ingest); err != nil {
		t.Fatalf("backdate created_at: %v", err)
	}

	// As of February 2026, the fact was not yet true → excluded.
	before := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	got, err := c.Recall(ctx, "Stripe", WithAsOf(before))
	if err != nil {
		t.Fatalf("Recall before: %v", err)
	}
	for _, r := range got {
		if r.Content == "Alice joined Stripe" {
			t.Fatalf("memory should be excluded as-of %v, but was returned", before)
		}
	}

	// As of April 2026, the fact was true → included.
	after := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	got, err = c.Recall(ctx, "Stripe", WithAsOf(after))
	if err != nil {
		t.Fatalf("Recall after: %v", err)
	}
	var found bool
	for _, r := range got {
		if r.Content == "Alice joined Stripe" {
			found = true
		}
	}
	if !found {
		t.Fatalf("memory should be included as-of %v, but was not returned", after)
	}
}
