package cortex

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/sausheong/cortex/internal/model"
)

func TestPutAndSearchMemory(t *testing.T) { forEachMode(t, testPutAndSearchMemory) }

func testPutAndSearchMemory(t *testing.T) {
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

func TestGetMemoriesByEntity(t *testing.T) { forEachMode(t, testGetMemoriesByEntity) }

func testGetMemoriesByEntity(t *testing.T) {
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
	forEachMode(t, testPutMemory_ConfidenceDefaultsToOne)
}

func testPutMemory_ConfidenceDefaultsToOne(t *testing.T) {
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

func TestPutMemory_ConfidenceClamped(t *testing.T) { forEachMode(t, testPutMemory_ConfidenceClamped) }

func testPutMemory_ConfidenceClamped(t *testing.T) {
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

// Legacy only: reads or writes plaintext columns with raw SQL.
func TestPutMemory_PopulatesFTS(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	m := &Memory{Content: "Alice migrated the billing service to Stripe"}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}

	// The generated tsv column must match the memory.
	var got string
	err := c.h.DB.QueryRowContext(ctx,
		`SELECT content FROM memories WHERE tsv @@ plainto_tsquery('english', ?)`, "Stripe",
	).Scan(&got)
	if err != nil {
		t.Fatalf("expected FTS match for memory, got error: %v", err)
	}
	if got != m.Content {
		t.Fatalf("FTS content mismatch: got %q want %q", got, m.Content)
	}
}

func TestSearchMemoriesOddQueries(t *testing.T) { forEachMode(t, testSearchMemoriesOddQueries) }

func testSearchMemoriesOddQueries(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	if err := c.PutMemory(ctx, &Memory{Content: "Alice joined Stripe"}); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}
	for _, q := range []string{"the", "C++", "\"quoted\"", ""} {
		got, err := c.SearchMemories(ctx, q, 10)
		if err != nil {
			t.Errorf("SearchMemories(%q) error: %v", q, err)
			continue
		}
		if q == "the" && len(got) != 0 {
			t.Errorf("stopword-only query returned %d results, want 0", len(got))
		}
	}
	for _, q := range []string{"the", "C++", "\"quoted\"", ""} {
		if _, err := c.SearchKeyword(ctx, q, 10); err != nil {
			t.Errorf("SearchKeyword(%q) error: %v", q, err)
		}
	}
}

func TestSearchMemoriesOrSemantics(t *testing.T) { forEachMode(t, testSearchMemoriesOrSemantics) }

func testSearchMemoriesOrSemantics(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	if err := c.PutMemory(ctx, &Memory{Content: "Alice joined Stripe"}); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}
	got, err := c.SearchMemories(ctx, "Stripe nonexistentword", 10)
	if err != nil {
		t.Fatalf("SearchMemories: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d results, want 1", len(got))
	}
}

func TestSearchMemories_RanksByRelevance(t *testing.T) {
	forEachMode(t, testSearchMemories_RanksByRelevance)
}

func testSearchMemories_RanksByRelevance(t *testing.T) {
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

func TestMemory_TemporalColumnsRoundTrip(t *testing.T) {
	forEachMode(t, testMemory_TemporalColumnsRoundTrip)
}

func testMemory_TemporalColumnsRoundTrip(t *testing.T) {
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

func TestInvalidateMemory(t *testing.T) { forEachMode(t, testInvalidateMemory) }

func testInvalidateMemory(t *testing.T) {
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
	err := c.h.DB.QueryRowContext(ctx,
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

func TestInvalidateMemory_NotFound(t *testing.T) { forEachMode(t, testInvalidateMemory_NotFound) }

func testInvalidateMemory_NotFound(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	if err := c.InvalidateMemory(ctx, "nonexistent-id", nil); err == nil {
		t.Fatal("expected error for nonexistent memory, got nil")
	}
}

func TestSearchMemories_HidesInvalidated(t *testing.T) {
	forEachMode(t, testSearchMemories_HidesInvalidated)
}

func testSearchMemories_HidesInvalidated(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	stale := &Memory{Content: "Alice's budget is 5000"}
	if err := c.PutMemory(ctx, stale); err != nil {
		t.Fatal(err)
	}
	current := &Memory{Content: "Alice's budget is 10000"}
	if err := c.PutMemory(ctx, current); err != nil {
		t.Fatal(err)
	}

	// Before invalidation: both match.
	got, err := c.SearchMemories(ctx, "budget", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 before invalidation, got %d", len(got))
	}

	// Invalidate the stale one.
	if err := c.InvalidateMemory(ctx, stale.ID, nil); err != nil {
		t.Fatal(err)
	}

	got, err = c.SearchMemories(ctx, "budget", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != current.ID {
		t.Fatalf("expected only the current memory after invalidation, got %d results", len(got))
	}
}

func TestPutMemory_PersistsValidAt(t *testing.T) { forEachMode(t, testPutMemory_PersistsValidAt) }

func testPutMemory_PersistsValidAt(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	when := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	m := &Memory{Content: "Alice joined Stripe", ValidAt: &when}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}

	got, err := c.getMemoryByID(ctx, m.ID)
	if err != nil {
		t.Fatalf("getMemoryByID: %v", err)
	}
	if got == nil {
		t.Fatal("expected memory, got nil")
	}
	if got.ValidAt == nil {
		t.Fatal("expected ValidAt to be persisted, got nil")
	}
	if !got.ValidAt.Equal(when) {
		t.Fatalf("ValidAt mismatch: got %v want %v", got.ValidAt, when)
	}
	// A memory with only valid_at set must still be currently valid (not retired).
	if got.InvalidAt != nil || got.ExpiredAt != nil {
		t.Fatalf("expected invalid_at/expired_at nil, got invalid=%v expired=%v", got.InvalidAt, got.ExpiredAt)
	}
}

func TestPutMemory_PersistsSpeaker(t *testing.T) { forEachMode(t, testPutMemory_PersistsSpeaker) }

func testPutMemory_PersistsSpeaker(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	m := &Memory{Content: "Prefers async standups", Speaker: "user"}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}

	got, err := c.getMemoryByID(ctx, m.ID)
	if err != nil {
		t.Fatalf("getMemoryByID: %v", err)
	}
	if got == nil || got.Speaker != "user" {
		t.Fatalf("expected speaker 'user', got %+v", got)
	}

	// And via FTS search read path.
	found, err := c.SearchMemories(ctx, "async standups", 10)
	if err != nil {
		t.Fatalf("SearchMemories: %v", err)
	}
	if len(found) != 1 || found[0].Speaker != "user" {
		t.Fatalf("expected speaker via search, got %+v", found)
	}
}

func TestSearchMemoriesMode_EventTimeWindow(t *testing.T) {
	forEachMode(t, testSearchMemoriesMode_EventTimeWindow)
}

func testSearchMemoriesMode_EventTimeWindow(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	// Two memories with different event-time starts.
	q1 := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	q3 := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	if err := c.PutMemory(ctx, &Memory{Content: "budget set to 5000 in January", ValidAt: &q1}); err != nil {
		t.Fatal(err)
	}
	if err := c.PutMemory(ctx, &Memory{Content: "budget set to 9000 in March", ValidAt: &q3}); err != nil {
		t.Fatal(err)
	}

	// Window covering only Q1 (Jan–Feb).
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	mode := model.TemporalMode{RangeFrom: &from, RangeTo: &to}

	got, err := c.searchMemoriesMode(ctx, "budget", 10, mode)
	if err != nil {
		t.Fatalf("searchMemoriesMode: %v", err)
	}
	if len(got) != 1 || got[0].Content != "budget set to 5000 in January" {
		t.Fatalf("expected only the January memory in the Q1 window, got %+v", got)
	}
}

func TestMemory_StaticColumnExistsAndDefaultsFalse(t *testing.T) {
	forEachMode(t, testMemory_StaticColumnExistsAndDefaultsFalse)
}

func testMemory_StaticColumnExistsAndDefaultsFalse(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Type: "person", Name: "Alice"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	// A memory written without setting Static must come back Static=false.
	m := &Memory{Content: "Alice exists", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetMemoriesByEntity(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d memories, want 1", len(got))
	}
	if got[0].Static {
		t.Errorf("expected Static=false by default, got true")
	}
}

func TestMemory_StaticRoundTrips(t *testing.T) { forEachMode(t, testMemory_StaticRoundTrips) }

func testMemory_StaticRoundTrips(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Type: "person", Name: "Bob"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	m := &Memory{Content: "Bob is from Seattle", EntityIDs: []string{e.ID}, Static: true}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}

	// via GetMemoriesByEntity
	got, err := c.GetMemoriesByEntity(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].Static {
		t.Fatalf("GetMemoriesByEntity: expected Static=true, got %+v", got)
	}

	// via SearchMemories (FTS)
	found, err := c.SearchMemories(ctx, "Seattle", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) == 0 || !found[0].Static {
		t.Fatalf("SearchMemories: expected Static=true, got %+v", found)
	}
}

func TestMemory_ForgetAfterColumnExistsAndDefaultsNil(t *testing.T) {
	forEachMode(t, testMemory_ForgetAfterColumnExistsAndDefaultsNil)
}

func testMemory_ForgetAfterColumnExistsAndDefaultsNil(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Type: "person", Name: "Alice"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	m := &Memory{Content: "Alice exists", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetMemoriesByEntity(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d memories, want 1", len(got))
	}
	if got[0].ForgetAfter != nil {
		t.Errorf("expected ForgetAfter=nil by default, got %v", got[0].ForgetAfter)
	}
}

func TestMemory_ForgetAfterRoundTrips(t *testing.T) { forEachMode(t, testMemory_ForgetAfterRoundTrips) }

func testMemory_ForgetAfterRoundTrips(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Type: "person", Name: "Bob"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	fa := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	m := &Memory{Content: "Bob has a deadline on Dec 31", EntityIDs: []string{e.ID}, ForgetAfter: &fa}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}

	got, err := c.GetMemoriesByEntity(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ForgetAfter == nil {
		t.Fatalf("GetMemoriesByEntity: expected ForgetAfter set, got %+v", got)
	}
	if !got[0].ForgetAfter.Equal(fa) {
		t.Errorf("ForgetAfter = %v, want %v", got[0].ForgetAfter, fa)
	}

	found, err := c.SearchMemories(ctx, "deadline", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) == 0 || found[0].ForgetAfter == nil || !found[0].ForgetAfter.Equal(fa) {
		t.Fatalf("SearchMemories: expected ForgetAfter=%v, got %+v", fa, found)
	}
}

// A rare query token must outrank a common one: the memory holding the
// distinctive keyword wins over memories that only share a frequent word.
func TestSearchMemories_RareTokenOutranksCommon(t *testing.T) {
	forEachMode(t, testSearchMemories_RareTokenOutranksCommon)
}

func testSearchMemories_RareTokenOutranksCommon(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	for _, content := range []string{
		"The team meeting discussed the budget and the schedule for the project",
		"The team meeting covered the project schedule and the budget review",
		"The team meeting agenda includes the project budget and schedule",
		"Zanzibar vendor contract was renewed",
		"Alice prefers dark roast coffee",
		"Bob moved to Singapore last spring",
		"The garden needs watering twice a week",
		"Carol booked flights to Lisbon",
	} {
		if err := c.PutMemory(ctx, &Memory{Content: content}); err != nil {
			t.Fatalf("PutMemory: %v", err)
		}
	}
	got, err := c.SearchMemories(ctx, "meeting Zanzibar", 10)
	if err != nil {
		t.Fatalf("SearchMemories: %v", err)
	}
	if len(got) == 0 || got[0].Content != "Zanzibar vendor contract was renewed" {
		t.Fatalf("rare-token memory should rank first, got %d results, first=%q", len(got), got[0].Content)
	}
}
