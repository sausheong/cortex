package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/sausheong/cortex"
)

const defaultExtractionPrompt = `Analyze the following text and extract structured knowledge.
Return a JSON object with the following fields:
- "entities": array of objects with "name" (string) and "type" (string)
- "relationships": array of objects with "source" (string, entity name), "target" (string, entity name), and "type" (string)
- "memories": array of objects with "content" (string, a concise factual statement)

Extract all people, organizations, places, concepts, and other notable entities.
Identify relationships between entities (e.g., works_at, knows, located_in).
Create memories for key facts and statements.

Return ONLY valid JSON, no markdown formatting or code fences.`

const decomposePrompt = `Decompose the following query into one or more structured sub-queries for a knowledge graph.
Return a JSON object with a "queries" array, where each query has:
- "type": one of "keyword_search", "memory_lookup", "vector_search", "graph_traverse"
- "params": object with at least a "query" string field

Example output:
{"queries": [{"type": "keyword_search", "params": {"query": "some text"}}, {"type": "memory_lookup", "params": {"query": "some text"}}]}

Return ONLY valid JSON, no markdown formatting or code fences.`

// LLMOption configures the Anthropic LLM.
type LLMOption func(*LLM)

// WithModel sets the Claude model to use (e.g., "claude-sonnet-4-5", "claude-haiku-4-5").
func WithModel(model string) LLMOption {
	return func(l *LLM) {
		l.model = anthropic.Model(model)
	}
}

// WithBaseURL sets a custom base URL for Anthropic-compatible APIs.
func WithBaseURL(url string) LLMOption {
	return func(l *LLM) {
		l.baseURL = url
	}
}

// WithMaxTokens sets the max tokens for responses (default 4096).
func WithMaxTokens(n int64) LLMOption {
	return func(l *LLM) {
		l.maxTokens = n
	}
}

// LLM implements the cortex.LLM interface using Anthropic's Claude API.
// Works with any Anthropic-compatible API by setting a custom base URL.
type LLM struct {
	client    anthropic.Client
	model     anthropic.Model
	baseURL   string
	maxTokens int64
}

// NewLLM creates a new Anthropic LLM provider. Defaults to Claude Sonnet 4.5.
// Use WithBaseURL to point at any Anthropic-compatible API.
func NewLLM(apiKey string, opts ...LLMOption) *LLM {
	l := &LLM{
		model:     anthropic.Model("claude-sonnet-4-5-20250929"),
		maxTokens: 4096,
	}
	for _, o := range opts {
		o(l)
	}

	clientOpts := []option.RequestOption{
		option.WithAPIKey(apiKey),
	}
	if l.baseURL != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(l.baseURL))
	}
	l.client = anthropic.NewClient(clientOpts...)

	return l
}

// Extract calls Claude to extract entities, relationships, and memories from text.
func (l *LLM) Extract(ctx context.Context, text string, prompt string) (cortex.ExtractionResult, error) {
	if prompt == "" {
		prompt = defaultExtractionPrompt
	}

	msg, err := l.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     l.model,
		MaxTokens: l.maxTokens,
		System: []anthropic.TextBlockParam{
			{Text: prompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(text)),
		},
	})
	if err != nil {
		return cortex.ExtractionResult{}, fmt.Errorf("anthropic: messages: %w", err)
	}

	raw := extractText(msg)
	parsed, err := parseExtractionJSON(raw)
	if err != nil {
		return cortex.ExtractionResult{Raw: raw}, fmt.Errorf("anthropic: parse extraction: %w", err)
	}

	return cortex.ExtractionResult{Raw: raw, Parsed: parsed}, nil
}

// Decompose calls Claude to break a query into structured sub-queries.
func (l *LLM) Decompose(ctx context.Context, query string) ([]cortex.StructuredQuery, error) {
	msg, err := l.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     l.model,
		MaxTokens: l.maxTokens,
		System: []anthropic.TextBlockParam{
			{Text: decomposePrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(query)),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("anthropic: decompose: %w", err)
	}

	raw := extractText(msg)
	var result struct {
		Queries []cortex.StructuredQuery `json:"queries"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("anthropic: parse decompose: %w", err)
	}

	return result.Queries, nil
}

// Summarize calls Claude to summarize the given texts.
func (l *LLM) Summarize(ctx context.Context, texts []string) (string, error) {
	combined := strings.Join(texts, "\n\n---\n\n")

	msg, err := l.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     l.model,
		MaxTokens: l.maxTokens,
		System: []anthropic.TextBlockParam{
			{Text: "Summarize the following texts into a concise summary. Return only the summary text, no formatting."},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(combined)),
		},
	})
	if err != nil {
		return "", fmt.Errorf("anthropic: summarize: %w", err)
	}

	return extractText(msg), nil
}

// extractText pulls the text content from a Claude message response.
func extractText(msg *anthropic.Message) string {
	var parts []string
	for _, block := range msg.Content {
		if block.Type == "text" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "")
}

// extractionJSON is the intermediate JSON structure from the LLM.
type extractionJSON struct {
	Entities []struct {
		Type string `json:"type"`
		Name string `json:"name"`
	} `json:"entities"`
	Relationships []struct {
		Source string `json:"source"`
		Target string `json:"target"`
		Type   string `json:"type"`
	} `json:"relationships"`
	Memories []json.RawMessage `json:"memories"`
}

// parseExtractionJSON parses Claude's JSON output into a cortex.Extraction.
func parseExtractionJSON(raw string) (*cortex.Extraction, error) {
	// Strip markdown code fences if present
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		if len(lines) > 2 {
			lines = lines[1 : len(lines)-1]
			raw = strings.Join(lines, "\n")
		}
	}

	var ej extractionJSON
	if err := json.Unmarshal([]byte(raw), &ej); err != nil {
		return nil, err
	}

	extraction := &cortex.Extraction{}

	for _, e := range ej.Entities {
		extraction.Entities = append(extraction.Entities, cortex.Entity{
			Type: e.Type,
			Name: e.Name,
		})
	}

	for _, r := range ej.Relationships {
		extraction.Relationships = append(extraction.Relationships, cortex.Relationship{
			SourceID: r.Source,
			TargetID: r.Target,
			Type:     r.Type,
		})
	}

	for _, m := range ej.Memories {
		var memObj struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(m, &memObj); err == nil && memObj.Content != "" {
			extraction.Memories = append(extraction.Memories, cortex.Memory{
				Content: memObj.Content,
			})
			continue
		}
		var memStr string
		if err := json.Unmarshal(m, &memStr); err == nil && memStr != "" {
			extraction.Memories = append(extraction.Memories, cortex.Memory{
				Content: memStr,
			})
		}
	}

	return extraction, nil
}
