package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sausheong/cortex"

	oai "github.com/sashabaranov/go-openai"
)

// defaultExtractionPrompt instructs the LLM to return structured extraction data.
const defaultExtractionPrompt = `Analyze the following text and extract structured knowledge.
Return a JSON object with the following fields:
- "entities": array of objects with "name" (string) and "type" (string)
- "relationships": array of objects with "source" (string, entity name), "target" (string, entity name), and "type" (string)
- "memories": array of objects with "content" (string, a concise factual statement)

Extract all people, organizations, places, concepts, and other notable entities.
Identify relationships between entities (e.g., works_at, knows, located_in).
Create memories for key facts and statements.

Return ONLY valid JSON, no markdown formatting.`

// decomposePrompt instructs the LLM to decompose a query into sub-queries.
const decomposePrompt = `Decompose the following query into one or more structured sub-queries for a knowledge graph.
Return a JSON object with a "queries" array, where each query has:
- "type": one of "keyword_search", "memory_lookup", "vector_search", "graph_traverse"
- "params": object with at least a "query" string field

Example output:
{"queries": [{"type": "keyword_search", "params": {"query": "some text"}}, {"type": "memory_lookup", "params": {"query": "some text"}}]}

Return ONLY valid JSON, no markdown formatting.`

// LLMOption configures the OpenAI LLM.
type LLMOption func(*LLM)

// WithModel sets the model to use.
func WithModel(model string) LLMOption {
	return func(l *LLM) {
		l.model = model
	}
}

// WithBaseURL sets a custom base URL for OpenAI-compatible APIs
// (e.g., Ollama, vLLM, LM Studio, Together AI, Groq).
func WithBaseURL(url string) LLMOption {
	return func(l *LLM) {
		l.baseURL = url
	}
}

// LLM implements the cortex.LLM interface using OpenAI's API.
// Works with any OpenAI-compatible API by setting a custom base URL.
type LLM struct {
	client  *oai.Client
	model   string
	baseURL string
}

// NewLLM creates a new OpenAI LLM provider. Use WithBaseURL to point at
// any OpenAI-compatible API (Ollama, vLLM, LM Studio, Together AI, Groq, etc.).
func NewLLM(apiKey string, opts ...LLMOption) *LLM {
	l := &LLM{
		model: oai.GPT4oMini,
	}
	for _, o := range opts {
		o(l)
	}
	if l.baseURL != "" {
		cfg := oai.DefaultConfig(apiKey)
		cfg.BaseURL = l.baseURL
		l.client = oai.NewClientWithConfig(cfg)
	} else {
		l.client = oai.NewClient(apiKey)
	}
	return l
}

// Extract calls OpenAI chat completion to extract entities, relationships,
// and memories from the given text. The prompt parameter is used as the
// system message; if empty, the defaultExtractionPrompt is used.
func (l *LLM) Extract(ctx context.Context, text string, prompt string) (cortex.ExtractionResult, error) {
	if prompt == "" {
		prompt = defaultExtractionPrompt
	}

	resp, err := l.client.CreateChatCompletion(ctx, oai.ChatCompletionRequest{
		Model: l.model,
		Messages: []oai.ChatCompletionMessage{
			{Role: oai.ChatMessageRoleSystem, Content: prompt},
			{Role: oai.ChatMessageRoleUser, Content: text},
		},
		ResponseFormat: &oai.ChatCompletionResponseFormat{
			Type: oai.ChatCompletionResponseFormatTypeJSONObject,
		},
	})
	if err != nil {
		return cortex.ExtractionResult{}, fmt.Errorf("openai: chat completion: %w", err)
	}

	if len(resp.Choices) == 0 {
		return cortex.ExtractionResult{}, fmt.Errorf("openai: no choices in response")
	}

	raw := resp.Choices[0].Message.Content

	// Parse the JSON response.
	parsed, err := parseExtractionJSON(raw)
	if err != nil {
		return cortex.ExtractionResult{Raw: raw}, fmt.Errorf("openai: parse extraction: %w", err)
	}

	return cortex.ExtractionResult{
		Raw:    raw,
		Parsed: parsed,
	}, nil
}

// Decompose calls OpenAI to break a query into structured sub-queries.
func (l *LLM) Decompose(ctx context.Context, query string) ([]cortex.StructuredQuery, error) {
	resp, err := l.client.CreateChatCompletion(ctx, oai.ChatCompletionRequest{
		Model: l.model,
		Messages: []oai.ChatCompletionMessage{
			{Role: oai.ChatMessageRoleSystem, Content: decomposePrompt},
			{Role: oai.ChatMessageRoleUser, Content: query},
		},
		ResponseFormat: &oai.ChatCompletionResponseFormat{
			Type: oai.ChatCompletionResponseFormatTypeJSONObject,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("openai: decompose: %w", err)
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("openai: no choices in decompose response")
	}

	raw := resp.Choices[0].Message.Content

	var result struct {
		Queries []cortex.StructuredQuery `json:"queries"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("openai: parse decompose response: %w", err)
	}

	return result.Queries, nil
}

// Summarize calls OpenAI to summarize the given texts into a single summary.
func (l *LLM) Summarize(ctx context.Context, texts []string) (string, error) {
	combined := strings.Join(texts, "\n\n---\n\n")
	prompt := "Summarize the following texts into a concise summary. Return only the summary text, no formatting."

	resp, err := l.client.CreateChatCompletion(ctx, oai.ChatCompletionRequest{
		Model: l.model,
		Messages: []oai.ChatCompletionMessage{
			{Role: oai.ChatMessageRoleSystem, Content: prompt},
			{Role: oai.ChatMessageRoleUser, Content: combined},
		},
	})
	if err != nil {
		return "", fmt.Errorf("openai: summarize: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("openai: no choices in summarize response")
	}

	return resp.Choices[0].Message.Content, nil
}

// extractionJSON is the intermediate JSON structure returned by the LLM.
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

// parseExtractionJSON parses the JSON output from the LLM extraction into
// a cortex.Extraction struct.
func parseExtractionJSON(raw string) (*cortex.Extraction, error) {
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
		// Handle case where memory is just a string.
		var memStr string
		if err := json.Unmarshal(m, &memStr); err == nil && memStr != "" {
			extraction.Memories = append(extraction.Memories, cortex.Memory{
				Content: memStr,
			})
		}
	}

	return extraction, nil
}
