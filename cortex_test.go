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
