package cortex

import (
	"context"
	"testing"
)

func TestPutAndSearchMemory(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	// Create entities to link to the memory.
	alice := &Entity{Type: "person", Name: "Alice", Source: "test"}
	bob := &Entity{Type: "person", Name: "Bob", Source: "test"}
	if err := c.PutEntity(ctx, alice); err != nil {
		t.Fatalf("PutEntity(Alice) error: %v", err)
	}
	if err := c.PutEntity(ctx, bob); err != nil {
		t.Fatalf("PutEntity(Bob) error: %v", err)
	}

	// Create a memory linked to both entities.
	mem := &Memory{
		Content:   "Alice and Bob met at the Go conference in 2024",
		EntityIDs: []string{alice.ID, bob.ID},
		Source:    "test",
	}
	if err := c.PutMemory(ctx, mem); err != nil {
		t.Fatalf("PutMemory() error: %v", err)
	}
	if mem.ID == "" {
		t.Fatal("expected memory ID to be set")
	}
	if mem.CreatedAt.IsZero() {
		t.Fatal("expected CreatedAt to be set")
	}

	// Search by keyword that appears in the memory.
	results, err := c.SearchMemories(ctx, "conference", 10)
	if err != nil {
		t.Fatalf("SearchMemories() error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != mem.ID {
		t.Errorf("expected memory ID %s, got %s", mem.ID, results[0].ID)
	}
	if results[0].Content != mem.Content {
		t.Errorf("expected content %q, got %q", mem.Content, results[0].Content)
	}

	// Verify entity links are loaded.
	if len(results[0].EntityIDs) != 2 {
		t.Fatalf("expected 2 entity IDs, got %d", len(results[0].EntityIDs))
	}

	// Search with multiple words — should match if any word appears.
	results2, err := c.SearchMemories(ctx, "Alice unknown", 10)
	if err != nil {
		t.Fatalf("SearchMemories(multi-word) error: %v", err)
	}
	if len(results2) != 1 {
		t.Fatalf("expected 1 result for multi-word search, got %d", len(results2))
	}

	// Search for something not in any memory.
	results3, err := c.SearchMemories(ctx, "quantum", 10)
	if err != nil {
		t.Fatalf("SearchMemories(no match) error: %v", err)
	}
	if len(results3) != 0 {
		t.Errorf("expected 0 results, got %d", len(results3))
	}
}

func TestGetMemoriesByEntity(t *testing.T) {
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

	// Memory linked to Alice only.
	mem1 := &Memory{
		Content:   "Alice presented at the conference",
		EntityIDs: []string{alice.ID},
		Source:    "test",
	}
	// Memory linked to both Alice and Bob.
	mem2 := &Memory{
		Content:   "Alice and Bob collaborated on a project",
		EntityIDs: []string{alice.ID, bob.ID},
		Source:    "test",
	}
	if err := c.PutMemory(ctx, mem1); err != nil {
		t.Fatalf("PutMemory(1) error: %v", err)
	}
	if err := c.PutMemory(ctx, mem2); err != nil {
		t.Fatalf("PutMemory(2) error: %v", err)
	}

	// Query memories for Alice — should find both.
	aliceMems, err := c.GetMemoriesByEntity(ctx, alice.ID)
	if err != nil {
		t.Fatalf("GetMemoriesByEntity(Alice) error: %v", err)
	}
	if len(aliceMems) != 2 {
		t.Fatalf("expected 2 memories for Alice, got %d", len(aliceMems))
	}

	// Query memories for Bob — should find only mem2.
	bobMems, err := c.GetMemoriesByEntity(ctx, bob.ID)
	if err != nil {
		t.Fatalf("GetMemoriesByEntity(Bob) error: %v", err)
	}
	if len(bobMems) != 1 {
		t.Fatalf("expected 1 memory for Bob, got %d", len(bobMems))
	}
	if bobMems[0].ID != mem2.ID {
		t.Errorf("expected memory ID %s, got %s", mem2.ID, bobMems[0].ID)
	}

	// Verify entity links are loaded on GetMemoriesByEntity results.
	if len(bobMems[0].EntityIDs) != 2 {
		t.Fatalf("expected 2 entity IDs on Bob's memory, got %d", len(bobMems[0].EntityIDs))
	}
}
