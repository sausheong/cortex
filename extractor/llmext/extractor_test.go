package llmext

import (
	"context"
	"testing"

	"github.com/sausheong/cortex"
)

// mockLLM is a test double that returns canned extraction results.
type mockLLM struct {
	result cortex.ExtractionResult
	err    error
}

func (m *mockLLM) Extract(_ context.Context, _ string, _ string) (cortex.ExtractionResult, error) {
	return m.result, m.err
}

func (m *mockLLM) Decompose(_ context.Context, _ string) ([]cortex.StructuredQuery, error) {
	return nil, nil
}

func (m *mockLLM) Summarize(_ context.Context, _ []string) (string, error) {
	return "", nil
}

func TestLLMExtractor(t *testing.T) {
	mock := &mockLLM{
		result: cortex.ExtractionResult{
			Raw: `{"entities":[...]}`,
			Parsed: &cortex.Extraction{
				Entities: []cortex.Entity{
					{Name: "Alice", Type: "person"},
					{Name: "Stripe", Type: "company"},
				},
				Relationships: []cortex.Relationship{
					{SourceID: "1", TargetID: "2", Type: "works_at"},
				},
				Memories: []cortex.Memory{
					{Content: "Alice works at Stripe"},
				},
			},
		},
	}

	ext := New(mock)
	result, err := ext.Extract(context.Background(), "Alice works at Stripe", "text/plain")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Entities) != 2 {
		t.Errorf("expected 2 entities, got %d", len(result.Entities))
	}
	if len(result.Relationships) != 1 {
		t.Errorf("expected 1 relationship, got %d", len(result.Relationships))
	}
	if len(result.Memories) != 1 {
		t.Errorf("expected 1 memory, got %d", len(result.Memories))
	}
}

func TestLLMExtractorNilParsed(t *testing.T) {
	mock := &mockLLM{
		result: cortex.ExtractionResult{
			Raw:    `unparseable`,
			Parsed: nil,
		},
	}

	ext := New(mock)
	result, err := ext.Extract(context.Background(), "some content", "text/plain")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Entities) != 0 {
		t.Errorf("expected 0 entities when Parsed is nil, got %d", len(result.Entities))
	}
	if len(result.Relationships) != 0 {
		t.Errorf("expected 0 relationships when Parsed is nil, got %d", len(result.Relationships))
	}
	if len(result.Memories) != 0 {
		t.Errorf("expected 0 memories when Parsed is nil, got %d", len(result.Memories))
	}
}
