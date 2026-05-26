package cortex

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func openTestDB(t *testing.T) *Cortex {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	c, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestPutAndGetEntity(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{
		Type:       "person",
		Name:       "Alice",
		Attributes: map[string]any{"age": 30, "role": "engineer"},
		Source:     "test",
	}

	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatalf("PutEntity() error: %v", err)
	}

	if e.ID == "" {
		t.Fatal("expected entity ID to be set")
	}

	got, err := c.GetEntity(ctx, e.ID)
	if err != nil {
		t.Fatalf("GetEntity() error: %v", err)
	}

	if got.Name != "Alice" {
		t.Errorf("Name = %q, want %q", got.Name, "Alice")
	}
	if got.Type != "person" {
		t.Errorf("Type = %q, want %q", got.Type, "person")
	}
	if got.Source != "test" {
		t.Errorf("Source = %q, want %q", got.Source, "test")
	}
	age, ok := got.Attributes["age"]
	if !ok {
		t.Fatal("expected 'age' in attributes")
	}
	// JSON numbers decode as float64
	if age.(float64) != 30 {
		t.Errorf("Attributes[age] = %v, want 30", age)
	}
	if got.CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be set")
	}
	if got.UpdatedAt.IsZero() {
		t.Error("expected UpdatedAt to be set")
	}
}

func TestPutEntityUpsertsByNameAndType(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e1 := &Entity{
		Type:       "person",
		Name:       "Bob",
		Attributes: map[string]any{"age": 25},
		Source:     "test",
	}
	if err := c.PutEntity(ctx, e1); err != nil {
		t.Fatalf("PutEntity() first call error: %v", err)
	}
	firstID := e1.ID

	e2 := &Entity{
		Type:       "person",
		Name:       "Bob",
		Attributes: map[string]any{"age": 26, "title": "senior"},
		Source:     "test-updated",
	}
	if err := c.PutEntity(ctx, e2); err != nil {
		t.Fatalf("PutEntity() second call error: %v", err)
	}

	if e2.ID != firstID {
		t.Errorf("expected same ID %q after upsert, got %q", firstID, e2.ID)
	}

	got, err := c.GetEntity(ctx, firstID)
	if err != nil {
		t.Fatalf("GetEntity() error: %v", err)
	}

	if got.Attributes["age"].(float64) != 26 {
		t.Errorf("expected updated age 26, got %v", got.Attributes["age"])
	}
	if got.Attributes["title"] != "senior" {
		t.Errorf("expected title 'senior', got %v", got.Attributes["title"])
	}
	if got.Source != "test-updated" {
		t.Errorf("expected updated source, got %q", got.Source)
	}
}

func TestGetEntityNotFound(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	_, err := c.GetEntity(ctx, "nonexistent-id")
	if err == nil {
		t.Fatal("expected error for nonexistent entity")
	}
}

func TestFindEntitiesByType(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	entities := []*Entity{
		{Type: "person", Name: "Alice", Source: "test"},
		{Type: "person", Name: "Bob", Source: "test"},
		{Type: "organization", Name: "Acme Corp", Source: "test"},
	}
	for _, e := range entities {
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatalf("PutEntity(%s) error: %v", e.Name, err)
		}
	}

	results, err := c.FindEntities(ctx, EntityFilter{Type: "person"})
	if err != nil {
		t.Fatalf("FindEntities() error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for _, r := range results {
		if r.Type != "person" {
			t.Errorf("expected type 'person', got %q", r.Type)
		}
	}
}

func TestFindEntitiesBySource(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	entities := []*Entity{
		{Type: "person", Name: "Alice", Source: "source-a"},
		{Type: "person", Name: "Bob", Source: "source-b"},
		{Type: "person", Name: "Carol", Source: "source-a"},
	}
	for _, e := range entities {
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatalf("PutEntity(%s) error: %v", e.Name, err)
		}
	}

	results, err := c.FindEntities(ctx, EntityFilter{Source: "source-a"})
	if err != nil {
		t.Fatalf("FindEntities(source-a) error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for _, r := range results {
		if r.Source != "source-a" {
			t.Errorf("expected source-a, got %q", r.Source)
		}
	}
}

func TestFindEntitiesNoFilter(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	for _, name := range []string{"Alice", "Bob", "Carol"} {
		e := &Entity{Type: "person", Name: name, Source: "test"}
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatalf("PutEntity(%s) error: %v", name, err)
		}
	}

	results, err := c.FindEntities(ctx, EntityFilter{})
	if err != nil {
		t.Fatalf("FindEntities(empty filter) error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results from empty filter, got %d", len(results))
	}
}

func TestFindEntitiesCombinedFilters(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	entities := []*Entity{
		{Type: "person", Name: "Alice Johnson", Source: "test"},
		{Type: "person", Name: "Alice Smith", Source: "test"},
		{Type: "organization", Name: "Alice Corp", Source: "test"},
		{Type: "person", Name: "Bob Brown", Source: "test"},
	}
	for _, e := range entities {
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatalf("PutEntity(%s) error: %v", e.Name, err)
		}
	}

	// Type=person AND name like Alice — should exclude Alice Corp (organization) and Bob.
	results, err := c.FindEntities(ctx, EntityFilter{Type: "person", NameLike: "%Alice%"})
	if err != nil {
		t.Fatalf("FindEntities() error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 person entities named Alice*, got %d", len(results))
	}
	for _, r := range results {
		if r.Type != "person" {
			t.Errorf("expected type person, got %q", r.Type)
		}
	}
}

func TestFindEntitiesByNameLike(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	entities := []*Entity{
		{Type: "person", Name: "Alice Johnson", Source: "test"},
		{Type: "person", Name: "Alice Smith", Source: "test"},
		{Type: "person", Name: "Bob Brown", Source: "test"},
	}
	for _, e := range entities {
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatalf("PutEntity(%s) error: %v", e.Name, err)
		}
	}

	results, err := c.FindEntities(ctx, EntityFilter{NameLike: "%Alice%"})
	if err != nil {
		t.Fatalf("FindEntities() error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for _, r := range results {
		if r.Name != "Alice Johnson" && r.Name != "Alice Smith" {
			t.Errorf("unexpected name %q", r.Name)
		}
	}
}

func TestPutEntity_ConfidenceDefaultsToOne(t *testing.T) {
	cx := openTestDB(t)
	e := &Entity{Type: "person", Name: "NoConfidenceSet"}
	if err := cx.PutEntity(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	got, err := cx.GetEntity(context.Background(), e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Confidence != 1.0 {
		t.Errorf("Confidence = %v, want 1.0 (zero coerces to 1)", got.Confidence)
	}
}

func TestPutEntity_ConfidenceRoundTrip(t *testing.T) {
	cx := openTestDB(t)
	e := &Entity{Type: "person", Name: "PartialConfidence", Confidence: 0.42}
	if err := cx.PutEntity(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	got, err := cx.GetEntity(context.Background(), e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Confidence != 0.42 {
		t.Errorf("Confidence = %v, want 0.42", got.Confidence)
	}
}

func TestPutEntity_ConfidenceClamped(t *testing.T) {
	cx := openTestDB(t)
	tests := []struct {
		in, want float64
	}{
		{1.5, 1.0},
		{-0.1, 0.0},
		{2.0, 1.0},
	}
	for _, tt := range tests {
		e := &Entity{Type: "person", Name: fmt.Sprintf("Clamp-%v", tt.in), Confidence: tt.in}
		if err := cx.PutEntity(context.Background(), e); err != nil {
			t.Fatal(err)
		}
		got, err := cx.GetEntity(context.Background(), e.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Confidence != tt.want {
			t.Errorf("input %v: Confidence = %v, want %v", tt.in, got.Confidence, tt.want)
		}
	}
}

// --- Basic merge ---

func TestMergeEntities_BasicMerge_ReTargetsEverything(t *testing.T) {
	cx := openTestDB(t)
	defer cx.Close()
	ctx := context.Background()

	keep := &Entity{Type: "person", Name: "Alice Keep"}
	drop := &Entity{Type: "person", Name: "Alice Drop"}
	other := &Entity{Type: "organization", Name: "Stripe"}
	for _, e := range []*Entity{keep, drop, other} {
		if err := cx.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	rel := &Relationship{SourceID: drop.ID, TargetID: other.ID, Type: "works_at"}
	if err := cx.PutRelationship(ctx, rel); err != nil {
		t.Fatal(err)
	}
	mem := &Memory{Content: "drop memory content", EntityIDs: []string{drop.ID}}
	if err := cx.PutMemory(ctx, mem); err != nil {
		t.Fatal(err)
	}
	ch := &Chunk{EntityID: drop.ID, Content: "drop chunk content"}
	if err := cx.PutChunk(ctx, ch); err != nil {
		t.Fatal(err)
	}

	stats, err := cx.MergeEntities(ctx, keep.ID, drop.ID)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if stats.Relationships != 1 {
		t.Errorf("Relationships = %d, want 1", stats.Relationships)
	}
	if stats.Memories != 1 {
		t.Errorf("Memories = %d, want 1", stats.Memories)
	}
	if stats.Chunks != 1 {
		t.Errorf("Chunks = %d, want 1", stats.Chunks)
	}

	if _, err := cx.GetEntity(ctx, drop.ID); err == nil {
		t.Error("drop entity should be deleted")
	}
	rels, err := cx.GetRelationships(ctx, keep.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundReTargeted := false
	for _, r := range rels {
		if r.SourceID == keep.ID && r.TargetID == other.ID && r.Type == "works_at" {
			foundReTargeted = true
		}
	}
	if !foundReTargeted {
		t.Error("re-targeted relationship not found on keep entity")
	}
}

// --- Validation errors ---

func TestMergeEntities_SelfMergeError(t *testing.T) {
	cx := openTestDB(t)
	defer cx.Close()
	ctx := context.Background()
	e := &Entity{Type: "person", Name: "Solo"}
	if err := cx.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	_, err := cx.MergeEntities(ctx, e.ID, e.ID)
	if err == nil {
		t.Fatal("expected error for self-merge")
	}
	if !strings.Contains(err.Error(), "itself") {
		t.Errorf("error %q should mention 'itself'", err.Error())
	}
}

func TestMergeEntities_KeepNotFound(t *testing.T) {
	cx := openTestDB(t)
	defer cx.Close()
	ctx := context.Background()
	drop := &Entity{Type: "person", Name: "Drop"}
	if err := cx.PutEntity(ctx, drop); err != nil {
		t.Fatal(err)
	}
	_, err := cx.MergeEntities(ctx, "ent_doesnt_exist", drop.ID)
	if err == nil {
		t.Fatal("expected error for missing keep")
	}
	if !strings.Contains(err.Error(), "keep entity not found") {
		t.Errorf("error %q should mention 'keep entity not found'", err.Error())
	}
}

func TestMergeEntities_DropNotFound(t *testing.T) {
	cx := openTestDB(t)
	defer cx.Close()
	ctx := context.Background()
	keep := &Entity{Type: "person", Name: "Keep"}
	if err := cx.PutEntity(ctx, keep); err != nil {
		t.Fatal(err)
	}
	_, err := cx.MergeEntities(ctx, keep.ID, "ent_doesnt_exist")
	if err == nil {
		t.Fatal("expected error for missing drop")
	}
	if !strings.Contains(err.Error(), "drop entity not found") {
		t.Errorf("error %q should mention 'drop entity not found'", err.Error())
	}
}

func TestMergeEntities_TypeMismatch(t *testing.T) {
	cx := openTestDB(t)
	defer cx.Close()
	ctx := context.Background()
	person := &Entity{Type: "person", Name: "Stripe"}
	org := &Entity{Type: "organization", Name: "Stripe Inc"}
	if err := cx.PutEntity(ctx, person); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutEntity(ctx, org); err != nil {
		t.Fatal(err)
	}
	_, err := cx.MergeEntities(ctx, person.ID, org.ID)
	if err == nil {
		t.Fatal("expected error for type mismatch")
	}
	if !strings.Contains(err.Error(), "cannot merge across types") {
		t.Errorf("error %q should mention 'cannot merge across types'", err.Error())
	}
}

// --- Dedup ---

func TestMergeEntities_RelationshipDedup(t *testing.T) {
	cx := openTestDB(t)
	defer cx.Close()
	ctx := context.Background()
	keep := &Entity{Type: "person", Name: "K"}
	drop := &Entity{Type: "person", Name: "D"}
	stripe := &Entity{Type: "organization", Name: "Stripe"}
	for _, e := range []*Entity{keep, drop, stripe} {
		if err := cx.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range []*Entity{keep, drop} {
		if err := cx.PutRelationship(ctx, &Relationship{SourceID: e.ID, TargetID: stripe.ID, Type: "works_at"}); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := cx.MergeEntities(ctx, keep.ID, drop.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.DupesDropped == 0 {
		t.Error("expected duplicate relationship to be dropped")
	}
	rels, _ := cx.GetRelationships(ctx, keep.ID, RelTypeFilter("works_at"))
	count := 0
	for _, r := range rels {
		if r.TargetID == stripe.ID {
			count++
		}
	}
	if count != 1 {
		t.Errorf("keep has %d works_at stripe relationships, want 1", count)
	}
}

func TestMergeEntities_MemoryEntitiesDedup(t *testing.T) {
	cx := openTestDB(t)
	defer cx.Close()
	ctx := context.Background()
	keep := &Entity{Type: "person", Name: "K"}
	drop := &Entity{Type: "person", Name: "D"}
	for _, e := range []*Entity{keep, drop} {
		if err := cx.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	mem := &Memory{Content: "shared memory", EntityIDs: []string{keep.ID, drop.ID}}
	if err := cx.PutMemory(ctx, mem); err != nil {
		t.Fatal(err)
	}
	stats, err := cx.MergeEntities(ctx, keep.ID, drop.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.DupesDropped == 0 {
		t.Error("expected duplicate memory link to be dropped")
	}
	mems, err := cx.GetMemoriesByEntity(ctx, keep.ID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, m := range mems {
		if m.Content == "shared memory" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 link to shared memory, got %d", count)
	}
}

// --- Self-loop cleanup ---

func TestMergeEntities_SelfLoopRemoved(t *testing.T) {
	cx := openTestDB(t)
	defer cx.Close()
	ctx := context.Background()
	keep := &Entity{Type: "person", Name: "K"}
	drop := &Entity{Type: "person", Name: "D"}
	for _, e := range []*Entity{keep, drop} {
		if err := cx.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := cx.PutRelationship(ctx, &Relationship{SourceID: drop.ID, TargetID: keep.ID, Type: "knows"}); err != nil {
		t.Fatal(err)
	}
	if _, err := cx.MergeEntities(ctx, keep.ID, drop.ID); err != nil {
		t.Fatal(err)
	}
	rels, _ := cx.GetRelationships(ctx, keep.ID)
	for _, r := range rels {
		if r.SourceID == keep.ID && r.TargetID == keep.ID {
			t.Errorf("found self-loop on keep entity: %+v", r)
		}
	}
}

// --- Attributes ---

func TestMergeEntities_AttributeUnion_KeepWins(t *testing.T) {
	cx := openTestDB(t)
	defer cx.Close()
	ctx := context.Background()
	keep := &Entity{
		Type:       "person",
		Name:       "K",
		Attributes: map[string]any{"role": "engineer"},
	}
	drop := &Entity{
		Type:       "person",
		Name:       "D",
		Attributes: map[string]any{"role": "developer", "team": "payments"},
	}
	for _, e := range []*Entity{keep, drop} {
		if err := cx.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := cx.MergeEntities(ctx, keep.ID, drop.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.AttrConflicts != 1 {
		t.Errorf("AttrConflicts = %d, want 1", stats.AttrConflicts)
	}
	got, err := cx.GetEntity(ctx, keep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Attributes["role"] != "engineer" {
		t.Errorf("role = %v, want engineer (keep wins)", got.Attributes["role"])
	}
	if got.Attributes["team"] != "payments" {
		t.Errorf("team = %v, want payments (unioned in)", got.Attributes["team"])
	}
	mf, ok := got.Attributes["merged_from"].([]any)
	if !ok {
		t.Fatalf("merged_from missing or wrong type: %T %v", got.Attributes["merged_from"], got.Attributes["merged_from"])
	}
	if len(mf) != 1 {
		t.Errorf("merged_from len = %d, want 1", len(mf))
	}
}

func TestMergeEntities_MergedFromAppends(t *testing.T) {
	cx := openTestDB(t)
	defer cx.Close()
	ctx := context.Background()
	keep := &Entity{Type: "person", Name: "K"}
	drop1 := &Entity{Type: "person", Name: "D1"}
	drop2 := &Entity{Type: "person", Name: "D2"}
	for _, e := range []*Entity{keep, drop1, drop2} {
		if err := cx.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := cx.MergeEntities(ctx, keep.ID, drop1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := cx.MergeEntities(ctx, keep.ID, drop2.ID); err != nil {
		t.Fatal(err)
	}
	got, err := cx.GetEntity(ctx, keep.ID)
	if err != nil {
		t.Fatal(err)
	}
	mf, ok := got.Attributes["merged_from"].([]any)
	if !ok {
		t.Fatalf("merged_from missing: %v", got.Attributes)
	}
	if len(mf) != 2 {
		t.Errorf("merged_from len = %d, want 2", len(mf))
	}
}

// --- Embeddings ---

func TestMergeEntities_StaleEmbeddingDropped(t *testing.T) {
	cx := openTestDB(t)
	defer cx.Close()
	ctx := context.Background()
	keep := &Entity{Type: "person", Name: "K"}
	drop := &Entity{Type: "person", Name: "D"}
	for _, e := range []*Entity{keep, drop} {
		if err := cx.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := cx.db.ExecContext(ctx,
		`INSERT INTO embeddings (id, ref_id, ref_type, vector) VALUES (?, ?, 'entity', ?)`,
		"emb_drop", drop.ID, []byte{0x00}); err != nil {
		t.Fatal(err)
	}
	stats, err := cx.MergeEntities(ctx, keep.ID, drop.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Embeddings != 1 {
		t.Errorf("Embeddings = %d, want 1", stats.Embeddings)
	}
	var count int
	if err := cx.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM embeddings WHERE ref_id = ?`, drop.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("embedding for drop entity still exists (count=%d)", count)
	}
}

// --- Idempotency ---

func TestMergeEntities_SecondCallReturnsDropNotFound(t *testing.T) {
	cx := openTestDB(t)
	defer cx.Close()
	ctx := context.Background()
	keep := &Entity{Type: "person", Name: "K"}
	drop := &Entity{Type: "person", Name: "D"}
	for _, e := range []*Entity{keep, drop} {
		if err := cx.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := cx.MergeEntities(ctx, keep.ID, drop.ID); err != nil {
		t.Fatal(err)
	}
	_, err := cx.MergeEntities(ctx, keep.ID, drop.ID)
	if err == nil {
		t.Fatal("expected error on second merge")
	}
	if !strings.Contains(err.Error(), "drop entity not found") {
		t.Errorf("error = %v, want 'drop entity not found'", err)
	}
}


// --- Dry-run ---

func TestMergeEntitiesDryRun_NoChanges(t *testing.T) {
	cx := openTestDB(t)
	defer cx.Close()
	ctx := context.Background()
	keep := &Entity{Type: "person", Name: "K"}
	drop := &Entity{Type: "person", Name: "D"}
	for _, e := range []*Entity{keep, drop} {
		if err := cx.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := cx.PutRelationship(ctx, &Relationship{SourceID: drop.ID, TargetID: keep.ID, Type: "knows"}); err != nil {
		t.Fatal(err)
	}
	stats, err := cx.MergeEntitiesDryRun(ctx, keep.ID, drop.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Relationships == 0 {
		t.Error("dry-run should report what would change")
	}
	// drop entity is STILL present.
	if _, err := cx.GetEntity(ctx, drop.ID); err != nil {
		t.Errorf("drop entity should still exist after dry-run: %v", err)
	}
}
