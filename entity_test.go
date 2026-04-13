package cortex

import (
	"context"
	"path/filepath"
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
