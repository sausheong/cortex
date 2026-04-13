package cortex

import (
	"context"
	"testing"
)

func TestPutAndGetRelationships(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	// Create two entities first.
	alice := &Entity{Type: "person", Name: "Alice", Source: "test"}
	bob := &Entity{Type: "person", Name: "Bob", Source: "test"}
	if err := c.PutEntity(ctx, alice); err != nil {
		t.Fatalf("PutEntity(Alice) error: %v", err)
	}
	if err := c.PutEntity(ctx, bob); err != nil {
		t.Fatalf("PutEntity(Bob) error: %v", err)
	}

	rel := &Relationship{
		SourceID:   alice.ID,
		TargetID:   bob.ID,
		Type:       "knows",
		Attributes: map[string]any{"since": 2020},
		Source:     "test",
	}
	if err := c.PutRelationship(ctx, rel); err != nil {
		t.Fatalf("PutRelationship() error: %v", err)
	}
	if rel.ID == "" {
		t.Fatal("expected relationship ID to be set")
	}

	// Query from source side.
	rels, err := c.GetRelationships(ctx, alice.ID)
	if err != nil {
		t.Fatalf("GetRelationships() error: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("expected 1 relationship, got %d", len(rels))
	}
	if rels[0].Type != "knows" {
		t.Errorf("Type = %q, want %q", rels[0].Type, "knows")
	}
	if rels[0].SourceID != alice.ID {
		t.Errorf("SourceID = %q, want %q", rels[0].SourceID, alice.ID)
	}
	if rels[0].TargetID != bob.ID {
		t.Errorf("TargetID = %q, want %q", rels[0].TargetID, bob.ID)
	}
	since, ok := rels[0].Attributes["since"]
	if !ok {
		t.Fatal("expected 'since' in attributes")
	}
	if since.(float64) != 2020 {
		t.Errorf("Attributes[since] = %v, want 2020", since)
	}
}

func TestGetRelationshipsFilterByType(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	alice := &Entity{Type: "person", Name: "Alice", Source: "test"}
	bob := &Entity{Type: "person", Name: "Bob", Source: "test"}
	acme := &Entity{Type: "organization", Name: "Acme", Source: "test"}
	if err := c.PutEntity(ctx, alice); err != nil {
		t.Fatalf("PutEntity(Alice) error: %v", err)
	}
	if err := c.PutEntity(ctx, bob); err != nil {
		t.Fatalf("PutEntity(Bob) error: %v", err)
	}
	if err := c.PutEntity(ctx, acme); err != nil {
		t.Fatalf("PutEntity(Acme) error: %v", err)
	}

	rel1 := &Relationship{SourceID: alice.ID, TargetID: bob.ID, Type: "knows", Source: "test"}
	rel2 := &Relationship{SourceID: alice.ID, TargetID: acme.ID, Type: "works_at", Source: "test"}
	if err := c.PutRelationship(ctx, rel1); err != nil {
		t.Fatalf("PutRelationship(knows) error: %v", err)
	}
	if err := c.PutRelationship(ctx, rel2); err != nil {
		t.Fatalf("PutRelationship(works_at) error: %v", err)
	}

	// Filter to "knows" only.
	rels, err := c.GetRelationships(ctx, alice.ID, RelTypeFilter("knows"))
	if err != nil {
		t.Fatalf("GetRelationships() error: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("expected 1 relationship, got %d", len(rels))
	}
	if rels[0].Type != "knows" {
		t.Errorf("Type = %q, want %q", rels[0].Type, "knows")
	}
}

func TestGetRelationshipsFromEitherDirection(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	alice := &Entity{Type: "person", Name: "Alice", Source: "test"}
	bob := &Entity{Type: "person", Name: "Bob", Source: "test"}
	if err := c.PutEntity(ctx, alice); err != nil {
		t.Fatalf("PutEntity(Alice) error: %v", err)
	}
	if err := c.PutEntity(ctx, bob); err != nil {
		t.Fatalf("PutEntity(Bob) error: %v", err)
	}

	rel := &Relationship{SourceID: alice.ID, TargetID: bob.ID, Type: "knows", Source: "test"}
	if err := c.PutRelationship(ctx, rel); err != nil {
		t.Fatalf("PutRelationship() error: %v", err)
	}

	// Query from target side — Bob should also see the relationship.
	rels, err := c.GetRelationships(ctx, bob.ID)
	if err != nil {
		t.Fatalf("GetRelationships(bob) error: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("expected 1 relationship from target side, got %d", len(rels))
	}
	if rels[0].Type != "knows" {
		t.Errorf("Type = %q, want %q", rels[0].Type, "knows")
	}
}
