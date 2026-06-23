package cortex

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
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

func TestPutMemory_ConfidenceDefaultsToOne(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()
	m := &Memory{Content: "alice did X"}
	if err := cx.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	results, err := cx.SearchMemories(ctx, "alice", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("no memories")
	}
	if results[0].Confidence != 1.0 {
		t.Errorf("Confidence = %v, want 1.0", results[0].Confidence)
	}
}

func TestPutMemory_ConfidenceClamped(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()
	m := &Memory{Content: "alice did Y", Confidence: -0.5}
	if err := cx.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	results, err := cx.SearchMemories(ctx, "alice", 10)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Confidence != 0.0 {
		t.Errorf("Confidence = %v, want 0.0 (clamped)", results[0].Confidence)
	}
}

func TestPutMemory_PopulatesFTS(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	m := &Memory{Content: "Alice migrated the billing service to Stripe"}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}

	// The FTS table must contain the memory, joinable by rowid.
	var got string
	err := c.db.QueryRowContext(ctx,
		`SELECT m.content FROM memories m
		 JOIN memories_fts f ON m.rowid = f.rowid
		 WHERE memories_fts MATCH ?`, "Stripe",
	).Scan(&got)
	if err != nil {
		t.Fatalf("expected FTS match for memory, got error: %v", err)
	}
	if got != m.Content {
		t.Fatalf("FTS content mismatch: got %q want %q", got, m.Content)
	}
}

func TestSearchMemories_RanksByRelevance(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	// Two memories; the query term appears in both, but one is a closer match.
	for _, content := range []string{
		"Bob mentioned coffee once in passing about Alice and Stripe and billing",
		"Alice prefers dark roast coffee",
	} {
		if err := c.PutMemory(ctx, &Memory{Content: content}); err != nil {
			t.Fatalf("PutMemory: %v", err)
		}
	}

	got, err := c.SearchMemories(ctx, "dark roast coffee", 10)
	if err != nil {
		t.Fatalf("SearchMemories: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected at least one result")
	}
	// FTS5 rank should put the focused "dark roast coffee" memory first.
	if got[0].Content != "Alice prefers dark roast coffee" {
		t.Fatalf("expected dark-roast memory ranked first, got %q", got[0].Content)
	}
}

func TestOpen_BackfillsMemoriesFTS(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// First open: insert a memory, then bypass FTS to simulate an old DB by
	// clearing the FTS table.
	c, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	if err := c.PutMemory(ctx, &Memory{Content: "legacy memory about Saturn"}); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}
	if _, err := c.db.ExecContext(ctx, `DELETE FROM memories_fts`); err != nil {
		t.Fatalf("clear fts: %v", err)
	}
	c.Close()

	// Reopen: backfill must repopulate FTS.
	c2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer c2.Close()

	got, err := c2.SearchMemories(ctx, "Saturn", 10)
	if err != nil {
		t.Fatalf("SearchMemories: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 backfilled result, got %d", len(got))
	}
}

func TestMemory_TemporalColumnsRoundTrip(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	m := &Memory{Content: "Alice's budget is 5000"}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}

	// Freshly ingested memory: temporal fields are nil (NULL in DB).
	got, err := c.SearchMemories(ctx, "budget", 10)
	if err != nil {
		t.Fatalf("SearchMemories: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 memory, got %d", len(got))
	}
	if got[0].ValidAt != nil || got[0].InvalidAt != nil || got[0].ExpiredAt != nil {
		t.Fatalf("expected nil temporal fields on fresh memory, got valid=%v invalid=%v expired=%v",
			got[0].ValidAt, got[0].InvalidAt, got[0].ExpiredAt)
	}
}

func TestInvalidateMemory(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	m := &Memory{Content: "Alice's budget is 5000"}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}

	eventTime := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if err := c.InvalidateMemory(ctx, m.ID, &eventTime); err != nil {
		t.Fatalf("InvalidateMemory: %v", err)
	}

	// Row still exists; expired_at set, invalid_at = eventTime.
	var expired, invalid sql.NullTime
	err := c.db.QueryRowContext(ctx,
		`SELECT expired_at, invalid_at FROM memories WHERE id = ?`, m.ID,
	).Scan(&expired, &invalid)
	if err != nil {
		t.Fatalf("memory row should still exist: %v", err)
	}
	if !expired.Valid {
		t.Fatal("expected expired_at to be set")
	}
	if !invalid.Valid || !invalid.Time.Equal(eventTime) {
		t.Fatalf("expected invalid_at = %v, got valid=%v time=%v", eventTime, invalid.Valid, invalid.Time)
	}
}

func TestInvalidateMemory_NotFound(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	if err := c.InvalidateMemory(ctx, "nonexistent-id", nil); err == nil {
		t.Fatal("expected error for nonexistent memory, got nil")
	}
}
