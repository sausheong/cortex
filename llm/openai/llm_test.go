package openai

import (
	"context"
	"os"
	"testing"
)

func TestExtractIntegration(t *testing.T) {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		t.Skip("OPENAI_API_KEY not set")
	}

	llm := NewLLM(key)
	ctx := context.Background()

	text := "Alice is a software engineer at Acme Corp. She works with Bob, who manages the engineering team."
	result, err := llm.Extract(ctx, text, "")
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}
	if result.Parsed == nil {
		t.Fatal("expected parsed extraction, got nil")
	}
	if len(result.Parsed.Entities) == 0 {
		t.Error("expected at least one entity")
	}
	t.Logf("Extracted %d entities, %d relationships, %d memories",
		len(result.Parsed.Entities),
		len(result.Parsed.Relationships),
		len(result.Parsed.Memories))
}

func TestDecomposeIntegration(t *testing.T) {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		t.Skip("OPENAI_API_KEY not set")
	}

	llm := NewLLM(key)
	ctx := context.Background()

	queries, err := llm.Decompose(ctx, "What does Alice do at Acme Corp?")
	if err != nil {
		t.Fatalf("Decompose failed: %v", err)
	}
	if len(queries) == 0 {
		t.Error("expected at least one sub-query")
	}
	for i, q := range queries {
		t.Logf("Query %d: type=%s params=%v", i, q.Type, q.Params)
	}
}

func TestSummarizeIntegration(t *testing.T) {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		t.Skip("OPENAI_API_KEY not set")
	}

	llm := NewLLM(key)
	ctx := context.Background()

	summary, err := llm.Summarize(ctx, []string{
		"Alice is a software engineer.",
		"Bob is a manager at Acme Corp.",
	})
	if err != nil {
		t.Fatalf("Summarize failed: %v", err)
	}
	if summary == "" {
		t.Error("expected non-empty summary")
	}
	t.Logf("Summary: %s", summary)
}

func TestParseExtractionJSON(t *testing.T) {
	raw := `{
		"entities": [
			{"type": "person", "name": "Alice"},
			{"type": "organization", "name": "Acme Corp"}
		],
		"relationships": [
			{"source": "Alice", "target": "Acme Corp", "type": "works_at"}
		],
		"memories": [
			{"content": "Alice works at Acme Corp"}
		]
	}`

	parsed, err := parseExtractionJSON(raw)
	if err != nil {
		t.Fatalf("parseExtractionJSON failed: %v", err)
	}

	if len(parsed.Entities) != 2 {
		t.Errorf("expected 2 entities, got %d", len(parsed.Entities))
	}
	if len(parsed.Relationships) != 1 {
		t.Errorf("expected 1 relationship, got %d", len(parsed.Relationships))
	}
	if len(parsed.Memories) != 1 {
		t.Errorf("expected 1 memory, got %d", len(parsed.Memories))
	}

	if parsed.Entities[0].Name != "Alice" {
		t.Errorf("expected entity name Alice, got %q", parsed.Entities[0].Name)
	}
	if parsed.Relationships[0].SourceID != "Alice" {
		t.Errorf("expected relationship source Alice, got %q", parsed.Relationships[0].SourceID)
	}
	if parsed.Memories[0].Content != "Alice works at Acme Corp" {
		t.Errorf("expected memory content, got %q", parsed.Memories[0].Content)
	}
}
