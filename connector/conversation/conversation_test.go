package conversation

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sausheong/cortex"
)

// mockExtractor returns predetermined entities from any input.
type mockExtractor struct {
	extractFn func(ctx context.Context, content, contentType string) (*cortex.Extraction, error)
}

func (m *mockExtractor) Extract(ctx context.Context, content, contentType string) (*cortex.Extraction, error) {
	if m.extractFn != nil {
		return m.extractFn(ctx, content, contentType)
	}
	return &cortex.Extraction{}, nil
}

func openTestDB(t *testing.T) *cortex.Cortex {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	cx, err := cortex.Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	t.Cleanup(func() { cx.Close() })
	return cx
}

func TestIngestSingleMessage(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	cx.SetExtractor(&mockExtractor{
		extractFn: func(_ context.Context, content, _ string) (*cortex.Extraction, error) {
			return &cortex.Extraction{
				Entities: []cortex.Entity{
					{Type: "person", Name: "Alice"},
					{Type: "organization", Name: "Stripe"},
				},
				Relationships: []cortex.Relationship{
					{SourceID: "Alice", TargetID: "Stripe", Type: "works_at"},
				},
				Memories: []cortex.Memory{
					{Content: "Alice works at Stripe"},
				},
			}, nil
		},
	})

	conn := New()
	msgs := []Message{
		{Role: "user", Content: "Alice works at Stripe as a staff engineer"},
	}

	if err := conn.Ingest(ctx, cx, msgs); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	// Verify entities were created.
	people, err := cx.FindEntities(ctx, cortex.EntityFilter{Type: "person"})
	if err != nil {
		t.Fatalf("FindEntities(person): %v", err)
	}
	if len(people) != 1 || people[0].Name != "Alice" {
		t.Errorf("expected person Alice, got %v", people)
	}

	orgs, err := cx.FindEntities(ctx, cortex.EntityFilter{Type: "organization"})
	if err != nil {
		t.Fatalf("FindEntities(organization): %v", err)
	}
	if len(orgs) != 1 || orgs[0].Name != "Stripe" {
		t.Errorf("expected org Stripe, got %v", orgs)
	}
}

func TestIngestMultipleMessages(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	var capturedContent string
	cx.SetExtractor(&mockExtractor{
		extractFn: func(_ context.Context, content, _ string) (*cortex.Extraction, error) {
			capturedContent = content
			return &cortex.Extraction{
				Entities: []cortex.Entity{
					{Type: "person", Name: "Alice"},
					{Type: "person", Name: "Bob"},
				},
				Memories: []cortex.Memory{
					{Content: "Alice and Bob discussed a project"},
				},
			}, nil
		},
	})

	conn := New()
	msgs := []Message{
		{Role: "user", Content: "Hi, I'm Alice"},
		{Role: "assistant", Content: "Hello Alice! How can I help?"},
		{Role: "user", Content: "Tell me about Bob's project"},
	}

	if err := conn.Ingest(ctx, cx, msgs); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	// Verify the content includes all three messages.
	if capturedContent == "" {
		t.Fatal("expected content to be passed to extractor")
	}
	for _, expected := range []string{"user: Hi, I'm Alice", "assistant: Hello Alice!", "user: Tell me about Bob"} {
		if !contains(capturedContent, expected) {
			t.Errorf("expected content to contain %q, got:\n%s", expected, capturedContent)
		}
	}

	// Verify entities were created.
	entities, err := cx.FindEntities(ctx, cortex.EntityFilter{Type: "person"})
	if err != nil {
		t.Fatalf("FindEntities: %v", err)
	}
	if len(entities) != 2 {
		t.Errorf("expected 2 person entities, got %d", len(entities))
	}
}

func TestIngestEmpty(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	// No extractor needed — empty messages should short-circuit.
	conn := New()

	if err := conn.Ingest(ctx, cx, nil); err != nil {
		t.Fatalf("Ingest(nil): %v", err)
	}
	if err := conn.Ingest(ctx, cx, []Message{}); err != nil {
		t.Fatalf("Ingest(empty): %v", err)
	}

	// Verify no entities were created.
	entities, err := cx.FindEntities(ctx, cortex.EntityFilter{})
	if err != nil {
		t.Fatalf("FindEntities: %v", err)
	}
	if len(entities) != 0 {
		t.Errorf("expected 0 entities, got %d", len(entities))
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
