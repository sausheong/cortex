package hybrid

import (
	"context"
	"testing"

	"github.com/sausheong/cortex"
)

// stubExtractor returns a fixed Extraction.
type stubExtractor struct {
	extraction *cortex.Extraction
	err        error
}

func (s *stubExtractor) Extract(_ context.Context, _ string, _ string) (*cortex.Extraction, error) {
	return s.extraction, s.err
}

func TestHybridMergesBothExtractors(t *testing.T) {
	det := &stubExtractor{
		extraction: &cortex.Extraction{
			Entities: []cortex.Entity{
				{Name: "Alice", Type: "person", Source: "frontmatter"},
			},
			Relationships: []cortex.Relationship{
				{SourceID: "1", TargetID: "2", Type: "knows"},
			},
			Memories: []cortex.Memory{
				{Content: "Alice is an engineer"},
			},
		},
	}

	llm := &stubExtractor{
		extraction: &cortex.Extraction{
			Entities: []cortex.Entity{
				{Name: "Stripe", Type: "company", Source: "llm"},
			},
			Relationships: []cortex.Relationship{
				{SourceID: "1", TargetID: "3", Type: "works_at"},
			},
			Memories: []cortex.Memory{
				{Content: "Alice works at Stripe"},
			},
		},
	}

	ext := New(det, llm)
	result, err := ext.Extract(context.Background(), "content", "text/plain")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Entities) != 2 {
		t.Errorf("expected 2 entities, got %d", len(result.Entities))
	}
	if len(result.Relationships) != 2 {
		t.Errorf("expected 2 relationships, got %d", len(result.Relationships))
	}
	if len(result.Memories) != 2 {
		t.Errorf("expected 2 memories, got %d", len(result.Memories))
	}
}

func TestHybridDeterministicError(t *testing.T) {
	det := &stubExtractor{
		err: context.Canceled,
	}
	llm := &stubExtractor{
		extraction: &cortex.Extraction{
			Entities: []cortex.Entity{{Name: "X"}},
		},
	}

	ext := New(det, llm)
	_, err := ext.Extract(context.Background(), "content", "text/plain")
	if err == nil {
		t.Fatal("expected error when deterministic extractor fails")
	}
}

func TestHybridLLMError(t *testing.T) {
	det := &stubExtractor{
		extraction: &cortex.Extraction{
			Entities: []cortex.Entity{{Name: "X"}},
		},
	}
	llm := &stubExtractor{
		err: context.Canceled,
	}

	ext := New(det, llm)
	_, err := ext.Extract(context.Background(), "content", "text/plain")
	if err == nil {
		t.Fatal("expected error when LLM extractor fails")
	}
}
