package openai

import (
	"context"
	"os"
	"testing"
	"time"
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

func TestParseExtractionJSONWithCodeFences(t *testing.T) {
	// Some OpenAI-compatible upstreams (Anthropic-via-LiteLLM most notably)
	// wrap their JSON response in markdown code fences. The parser must
	// strip them before json.Unmarshal.
	raw := "```json\n{\"entities\": [{\"type\": \"person\", \"name\": \"Alice\"}], \"relationships\": [], \"memories\": []}\n```"

	parsed, err := parseExtractionJSON(raw)
	if err != nil {
		t.Fatalf("parseExtractionJSON with fences: %v", err)
	}
	if len(parsed.Entities) != 1 {
		t.Errorf("got %d entities, want 1", len(parsed.Entities))
	}
}

func TestParseExtractionJSON_PreservesConfidence(t *testing.T) {
	raw := `{
		"entities": [
			{"name": "Alice", "type": "person", "confidence": 0.9}
		],
		"relationships": [
			{"source": "Alice", "target": "Stripe", "type": "works_at", "confidence": 0.6}
		],
		"memories": [
			{"content": "alice joined stripe", "confidence": 0.4}
		]
	}`
	ext, err := parseExtractionJSON(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ext.Entities) != 1 || ext.Entities[0].Confidence != 0.9 {
		t.Errorf("entity confidence = %v, want 0.9", ext.Entities[0].Confidence)
	}
	if len(ext.Relationships) != 1 || ext.Relationships[0].Confidence != 0.6 {
		t.Errorf("rel confidence = %v, want 0.6", ext.Relationships[0].Confidence)
	}
	if len(ext.Memories) != 1 || ext.Memories[0].Confidence != 0.4 {
		t.Errorf("memory confidence = %v, want 0.4", ext.Memories[0].Confidence)
	}
}

func TestParseExtractionJSON_OmittedConfidenceIsZero(t *testing.T) {
	// Pre-feature LLM response — no confidence field anywhere.
	raw := `{
		"entities": [{"name": "Bob", "type": "person"}],
		"relationships": [],
		"memories": [{"content": "bob exists"}]
	}`
	ext, err := parseExtractionJSON(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Parser returns zero — Put layer will coerce to 1.0.
	if ext.Entities[0].Confidence != 0 {
		t.Errorf("entity confidence = %v, want 0 (zero, to be coerced upstream)", ext.Entities[0].Confidence)
	}
	if ext.Memories[0].Confidence != 0 {
		t.Errorf("memory confidence = %v, want 0", ext.Memories[0].Confidence)
	}
}

func TestParseExtractionJSON_ReadsValidAt(t *testing.T) {
	raw := `{"entities":[],"relationships":[],"memories":[
		{"content":"Alice joined Stripe","valid_at":"2026-03"},
		{"content":"Bob likes tea"}
	]}`
	ex, err := parseExtractionJSON(raw)
	if err != nil {
		t.Fatalf("parseExtractionJSON: %v", err)
	}
	if len(ex.Memories) != 2 {
		t.Fatalf("expected 2 memories, got %d", len(ex.Memories))
	}
	if ex.Memories[0].ValidAt == nil {
		t.Fatal("expected ValidAt set on first memory")
	}
	want := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if !ex.Memories[0].ValidAt.Equal(want) {
		t.Fatalf("ValidAt = %v, want %v", ex.Memories[0].ValidAt, want)
	}
	if ex.Memories[1].ValidAt != nil {
		t.Fatal("expected nil ValidAt on memory without valid_at")
	}
}
