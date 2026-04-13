package anthropic

import (
	"context"
	"os"
	"testing"
)

func TestExtractIntegration(t *testing.T) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}

	llm := NewLLM(key)
	result, err := llm.Extract(context.Background(),
		"Alice works at Stripe as a staff engineer. She knows Bob from their time at Google.", "")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.Parsed == nil {
		t.Fatal("expected parsed extraction")
	}
	if len(result.Parsed.Entities) == 0 {
		t.Error("expected at least one entity")
	}
}

func TestDecomposeIntegration(t *testing.T) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}

	llm := NewLLM(key)
	queries, err := llm.Decompose(context.Background(), "What do I know about Alice?")
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	if len(queries) == 0 {
		t.Error("expected at least one sub-query")
	}
}

func TestParseExtractionJSON(t *testing.T) {
	raw := `{
		"entities": [
			{"type": "person", "name": "Alice"},
			{"type": "organization", "name": "Stripe"}
		],
		"relationships": [
			{"source": "Alice", "target": "Stripe", "type": "works_at"}
		],
		"memories": [
			{"content": "Alice works at Stripe"}
		]
	}`

	parsed, err := parseExtractionJSON(raw)
	if err != nil {
		t.Fatalf("parseExtractionJSON: %v", err)
	}
	if len(parsed.Entities) != 2 {
		t.Errorf("got %d entities, want 2", len(parsed.Entities))
	}
	if len(parsed.Relationships) != 1 {
		t.Errorf("got %d relationships, want 1", len(parsed.Relationships))
	}
	if len(parsed.Memories) != 1 {
		t.Errorf("got %d memories, want 1", len(parsed.Memories))
	}
}

func TestParseExtractionJSONWithCodeFences(t *testing.T) {
	raw := "```json\n{\"entities\": [{\"type\": \"person\", \"name\": \"Alice\"}], \"relationships\": [], \"memories\": []}\n```"

	parsed, err := parseExtractionJSON(raw)
	if err != nil {
		t.Fatalf("parseExtractionJSON with fences: %v", err)
	}
	if len(parsed.Entities) != 1 {
		t.Errorf("got %d entities, want 1", len(parsed.Entities))
	}
}

func TestParseExtractionJSONMemoryAsString(t *testing.T) {
	raw := `{"entities": [], "relationships": [], "memories": ["Alice works at Stripe"]}`

	parsed, err := parseExtractionJSON(raw)
	if err != nil {
		t.Fatalf("parseExtractionJSON: %v", err)
	}
	if len(parsed.Memories) != 1 {
		t.Errorf("got %d memories, want 1", len(parsed.Memories))
	}
	if parsed.Memories[0].Content != "Alice works at Stripe" {
		t.Errorf("memory content = %q, want %q", parsed.Memories[0].Content, "Alice works at Stripe")
	}
}
