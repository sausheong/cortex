package cortex

import (
	"context"
	"testing"
)

func TestTraverseOneLevel(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	// Setup: Alice -> Stripe (works_at), Alice -> Bob (knows).
	alice := &Entity{Type: "person", Name: "Alice", Source: "test"}
	bob := &Entity{Type: "person", Name: "Bob", Source: "test"}
	stripe := &Entity{Type: "organization", Name: "Stripe", Source: "test"}
	for _, e := range []*Entity{alice, bob, stripe} {
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatalf("PutEntity(%s) error: %v", e.Name, err)
		}
	}

	rel1 := &Relationship{SourceID: alice.ID, TargetID: stripe.ID, Type: "works_at", Source: "test"}
	rel2 := &Relationship{SourceID: alice.ID, TargetID: bob.ID, Type: "knows", Source: "test"}
	if err := c.PutRelationship(ctx, rel1); err != nil {
		t.Fatalf("PutRelationship(works_at) error: %v", err)
	}
	if err := c.PutRelationship(ctx, rel2); err != nil {
		t.Fatalf("PutRelationship(knows) error: %v", err)
	}

	// Traverse from Alice with default depth (1).
	graph, err := c.Traverse(ctx, alice.ID)
	if err != nil {
		t.Fatalf("Traverse() error: %v", err)
	}

	if len(graph.Entities) != 3 {
		t.Errorf("expected 3 entities, got %d", len(graph.Entities))
		for _, e := range graph.Entities {
			t.Logf("  entity: %s (%s)", e.Name, e.ID)
		}
	}
	if len(graph.Relationships) != 2 {
		t.Errorf("expected 2 relationships, got %d", len(graph.Relationships))
	}
}

func TestTraverseWithEdgeTypeFilter(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	alice := &Entity{Type: "person", Name: "Alice", Source: "test"}
	bob := &Entity{Type: "person", Name: "Bob", Source: "test"}
	stripe := &Entity{Type: "organization", Name: "Stripe", Source: "test"}
	for _, e := range []*Entity{alice, bob, stripe} {
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatalf("PutEntity(%s) error: %v", e.Name, err)
		}
	}

	rel1 := &Relationship{SourceID: alice.ID, TargetID: stripe.ID, Type: "works_at", Source: "test"}
	rel2 := &Relationship{SourceID: alice.ID, TargetID: bob.ID, Type: "knows", Source: "test"}
	if err := c.PutRelationship(ctx, rel1); err != nil {
		t.Fatalf("PutRelationship(works_at) error: %v", err)
	}
	if err := c.PutRelationship(ctx, rel2); err != nil {
		t.Fatalf("PutRelationship(knows) error: %v", err)
	}

	// Filter to "works_at" only — should get Alice and Stripe.
	graph, err := c.Traverse(ctx, alice.ID, WithEdgeTypes("works_at"))
	if err != nil {
		t.Fatalf("Traverse() error: %v", err)
	}

	if len(graph.Entities) != 2 {
		t.Errorf("expected 2 entities (Alice, Stripe), got %d", len(graph.Entities))
		for _, e := range graph.Entities {
			t.Logf("  entity: %s (%s)", e.Name, e.ID)
		}
	}

	if len(graph.Relationships) != 1 {
		t.Errorf("expected 1 relationship, got %d", len(graph.Relationships))
	}

	// Verify the entities are Alice and Stripe.
	names := make(map[string]bool)
	for _, e := range graph.Entities {
		names[e.Name] = true
	}
	if !names["Alice"] {
		t.Error("expected Alice in results")
	}
	if !names["Stripe"] {
		t.Error("expected Stripe in results")
	}
}

func TestTraverseDepthZero(t *testing.T) {
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

	// Depth 0 — should return only the start entity, no relationships.
	graph, err := c.Traverse(ctx, alice.ID, WithDepth(0))
	if err != nil {
		t.Fatalf("Traverse() error: %v", err)
	}

	if len(graph.Entities) != 1 {
		t.Errorf("expected 1 entity, got %d", len(graph.Entities))
	}
	if graph.Entities[0].Name != "Alice" {
		t.Errorf("expected entity Alice, got %s", graph.Entities[0].Name)
	}
	if len(graph.Relationships) != 0 {
		t.Errorf("expected 0 relationships, got %d", len(graph.Relationships))
	}
}
