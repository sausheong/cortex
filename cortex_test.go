package cortex

import (
	"context"
	"testing"
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
