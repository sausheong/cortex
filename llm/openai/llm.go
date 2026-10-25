package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sausheong/cortex"

	oai "github.com/sashabaranov/go-openai"
)

// defaultExtractionPrompt instructs the LLM to return structured extraction data.
const defaultExtractionPrompt = `Analyze the following text and extract structured knowledge.
Return a JSON object with the following fields:
- "entities": array of objects with "name" (string) and "type" (string)
- "relationships": array of objects with "source" (string, entity name), "target" (string, entity name), and "type" (string)
- "memories": array of objects with "content" (string, a concise factual statement), "about" (array of the entity names, from "entities", that the memory is about) and optional "valid_at" (string: when the fact became true in the world, e.g. "2026-03", "March 2026", "2026-03-15"; omit if the text gives no time)

Extract all people, organizations, places, concepts, and other notable entities.
Identify relationships between entities (e.g., works_at, knows, located_in).
Create memories for key facts and statements.

When a memory states when something became true (a start date, a join date, an "as of" date), set "valid_at" to that date. Do not guess; omit it if the text does not say.

- "forget_after": string (date) — set ONLY for memories that stop being relevant after a known point: appointments, deadlines, "today"/"tomorrow"/"this week", anything time-bound. Resolve relative phrases against today's date and return an absolute date (e.g. today is 2026-06-27, "tomorrow" -> "2026-06-28"). Omit for durable facts. A memory with forget_after should NOT be marked static.

Return ONLY valid JSON, no markdown formatting.`

// decomposePrompt instructs the LLM to decompose a query into sub-queries.
const decomposePrompt = `Decompose the following query into one or more structured sub-queries for a knowledge graph.
Return a JSON object with a "queries" array, where each query has:
- "type": one of "keyword_search", "memory_lookup", "vector_search", "graph_traverse", "temporal_filter"
- "params": object with at least a "query" string field

Example output:
{"queries": [{"type": "keyword_search", "params": {"query": "some text"}}, {"type": "memory_lookup", "params": {"query": "some text"}}]}

Use "temporal_filter" when the query refers to a time or period (e.g. "in Q1", "last year", "before the reorg", "in March 2026"). Its params take "query" plus optional "from" and "to" date strings (e.g. "2026-01-01", "2026-03"). Resolve relative references to absolute dates when you can; omit a bound you cannot determine.
Example: {"queries":[{"type":"temporal_filter","params":{"query":"budget","from":"2026-01-01","to":"2026-04-01"}}]}

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
	client     *oai.Client
	model      string
	baseURL    string
	httpClient *http.Client
}

// WithHTTPClient makes the LLM use hc for every request.
func WithHTTPClient(hc *http.Client) LLMOption {
	return func(l *LLM) { l.httpClient = hc }
}

// NewLLM creates a new OpenAI LLM provider. Use WithBaseURL to point at
// any OpenAI-compatible API (Ollama, vLLM, LM Studio, Together AI, Groq, etc.).
func NewLLM(apiKey string, opts ...LLMOption) *LLM {
	l := &LLM{
		model: "gpt-5.4-mini",
	}
	for _, o := range opts {
		o(l)
	}
	cfg := oai.DefaultConfig(apiKey)
	if l.baseURL != "" {
		cfg.BaseURL = l.baseURL
	}
	if l.httpClient != nil {
		cfg.HTTPClient = l.httpClient
	}
	l.client = oai.NewClientWithConfig(cfg)
	return l
}

// Extract calls OpenAI chat completion to extract entities, relationships,
// and memories from the given text. The prompt parameter is used as the
// system message; if empty, the defaultExtractionPrompt is used.
func (l *LLM) Extract(ctx context.Context, text string, prompt string) (cortex.ExtractionResult, error) {
	if prompt == "" {
		prompt = defaultExtractionPrompt
	}
	prompt = fmt.Sprintf("Today's date is %s.\n\n", time.Now().UTC().Format("2006-01-02")) + prompt

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

// detectConflictsPrompt instructs the LLM to flag contradicting memories.
const detectConflictsPrompt = `You are given a list of memories about the same subject, each with an ID.
Identify pairs where one memory CONTRADICTS or SUPERSEDES another (e.g. a value
changed, a status updated, a fact corrected). Do NOT flag memories that merely
add detail without contradicting.

Return ONLY valid JSON, no markdown:
{"conflicts":[{"stale_id":"<id of the outdated memory>","superseded_by_id":"<id of the newer memory that replaces it>","reason":"<short reason>"}]}

If there are no contradictions, return {"conflicts":[]}.`

// DetectConflicts implements cortex.Reconciler.
func (l *LLM) DetectConflicts(ctx context.Context, memories []cortex.Memory) ([]cortex.ConflictPair, error) {
	if len(memories) < 2 {
		return nil, nil
	}
	var sb strings.Builder
	for _, m := range memories {
		fmt.Fprintf(&sb, "ID %s: %s\n", m.ID, m.Content)
	}

	resp, err := l.client.CreateChatCompletion(ctx, oai.ChatCompletionRequest{
		Model: l.model,
		Messages: []oai.ChatCompletionMessage{
			{Role: oai.ChatMessageRoleSystem, Content: detectConflictsPrompt},
			{Role: oai.ChatMessageRoleUser, Content: sb.String()},
		},
		ResponseFormat: &oai.ChatCompletionResponseFormat{
			Type: oai.ChatCompletionResponseFormatTypeJSONObject,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("openai: detect conflicts: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("openai: no choices in detect conflicts response")
	}
	return parseConflictsJSON(resp.Choices[0].Message.Content)
}

func parseConflictsJSON(raw string) ([]cortex.ConflictPair, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		if len(lines) > 2 {
			raw = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}
	var parsed struct {
		Conflicts []cortex.ConflictPair `json:"conflicts"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("openai: parse conflicts: %w", err)
	}
	return parsed.Conflicts, nil
}

var _ cortex.Reconciler = (*LLM)(nil)

const detectRelationsPrompt = `You are given a list of memories about the same subject, each with an ID.
Identify pairs where one memory EXTENDS another (adds detail without contradicting)
or DERIVES from another (is inferred/follows from it). Do NOT flag contradictions
or simple restatements — only genuine elaboration or derivation.

Return ONLY valid JSON, no markdown:
{"relations":[{"source_id":"<id of the memory that extends/derives>","target_id":"<id of the base memory>","type":"extends|derives","reason":"<short reason>"}]}

For "extends", source adds detail to target. For "derives", source is inferred from target.
If there are none, return {"relations":[]}.`

// DetectRelations implements cortex.RelationDetector.
func (l *LLM) DetectRelations(ctx context.Context, memories []cortex.Memory) ([]cortex.MemoryRelation, error) {
	if len(memories) < 2 {
		return nil, nil
	}
	var sb strings.Builder
	for _, m := range memories {
		fmt.Fprintf(&sb, "ID %s: %s\n", m.ID, m.Content)
	}

	resp, err := l.client.CreateChatCompletion(ctx, oai.ChatCompletionRequest{
		Model: l.model,
		Messages: []oai.ChatCompletionMessage{
			{Role: oai.ChatMessageRoleSystem, Content: detectRelationsPrompt},
			{Role: oai.ChatMessageRoleUser, Content: sb.String()},
		},
		ResponseFormat: &oai.ChatCompletionResponseFormat{
			Type: oai.ChatCompletionResponseFormatTypeJSONObject,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("openai: detect relations: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("openai: no choices in detect relations response")
	}
	return parseRelationsJSON(resp.Choices[0].Message.Content)
}

func parseRelationsJSON(raw string) ([]cortex.MemoryRelation, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		if len(lines) > 2 {
			raw = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}
	var parsed struct {
		Relations []cortex.MemoryRelation `json:"relations"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("openai: parse relations: %w", err)
	}
	return parsed.Relations, nil
}

var _ cortex.RelationDetector = (*LLM)(nil)

// parseExtractionJSON parses the model's extraction output; see
// cortex.ParseExtractionJSON.
func parseExtractionJSON(raw string) (*cortex.Extraction, error) {
	return cortex.ParseExtractionJSON(raw)
}

// DisambiguateEntity implements cortex.EntityDisambiguator.
func (l *LLM) DisambiguateEntity(ctx context.Context, m cortex.EntityMention, candidates []cortex.Entity) (cortex.EntityChoice, error) {
	if len(candidates) == 0 {
		return cortex.EntityChoice{}, nil
	}
	resp, err := l.client.CreateChatCompletion(ctx, oai.ChatCompletionRequest{
		Model: l.model,
		Messages: []oai.ChatCompletionMessage{
			{Role: oai.ChatMessageRoleSystem, Content: cortex.DisambiguatePrompt},
			{Role: oai.ChatMessageRoleUser, Content: cortex.FormatDisambiguation(m, candidates)},
		},
		ResponseFormat: &oai.ChatCompletionResponseFormat{
			Type: oai.ChatCompletionResponseFormatTypeJSONObject,
		},
	})
	if err != nil {
		return cortex.EntityChoice{}, fmt.Errorf("openai: disambiguate entity: %w", err)
	}
	if len(resp.Choices) == 0 {
		return cortex.EntityChoice{}, fmt.Errorf("openai: no choices in disambiguate response")
	}
	return cortex.ParseEntityChoice(resp.Choices[0].Message.Content)
}

var _ cortex.EntityDisambiguator = (*LLM)(nil)

// schemaJSON adapts a pre-marshalled JSON Schema to json.Marshaler.
type schemaJSON []byte

func (s schemaJSON) MarshalJSON() ([]byte, error) { return s, nil }

// isResponseFormatRejection reports whether err is a 400 from a server that
// does not support strict json_schema response formats.
func isResponseFormatRejection(err error) bool {
	var status int
	var text string
	var apiErr *oai.APIError
	var reqErr *oai.RequestError
	switch {
	case errors.As(err, &apiErr):
		status, text = apiErr.HTTPStatusCode, apiErr.Message
	case errors.As(err, &reqErr):
		status, text = reqErr.HTTPStatusCode, string(reqErr.Body)+" "+reqErr.Error()
	default:
		return false
	}
	if status != http.StatusBadRequest {
		return false
	}
	text = strings.ToLower(text)
	return strings.Contains(text, "response_format") || strings.Contains(text, "json_schema")
}

// WriteArticle implements cortex.ArticleWriter. The system message carries
// only the caller's fixed instructions; all subject data is one JSON document
// in the user message. It asks for a strict json_schema response and, if the
// server rejects that with a 400, retries once in json_object mode with the
// schema stated in the system text. Cortex validates the result either way.
func (l *LLM) WriteArticle(ctx context.Context, system string, in cortex.ArticleInput, length string) (cortex.Article, cortex.ArticleUsage, error) {
	user, err := cortex.ArticleRequestJSON(in, length)
	if err != nil {
		return cortex.Article{}, cortex.ArticleUsage{}, fmt.Errorf("openai: encode article input: %w", err)
	}
	schema, err := json.Marshal(cortex.ArticleJSONSchema())
	if err != nil {
		return cortex.Article{}, cortex.ArticleUsage{}, fmt.Errorf("openai: encode article schema: %w", err)
	}
	req := oai.ChatCompletionRequest{
		Model: l.model,
		Messages: []oai.ChatCompletionMessage{
			{Role: oai.ChatMessageRoleSystem, Content: system},
			{Role: oai.ChatMessageRoleUser, Content: user},
		},
		ResponseFormat: &oai.ChatCompletionResponseFormat{
			Type: oai.ChatCompletionResponseFormatTypeJSONSchema,
			JSONSchema: &oai.ChatCompletionResponseFormatJSONSchema{
				Name:   "article",
				Schema: schemaJSON(schema),
				Strict: true,
			},
		},
	}
	resp, err := l.client.CreateChatCompletion(ctx, req)
	if err != nil && isResponseFormatRejection(err) {
		req.Messages[0].Content = system + "\n\nReturn ONLY one JSON object that conforms to this JSON Schema, with no other text:\n" + string(schema)
		req.ResponseFormat = &oai.ChatCompletionResponseFormat{Type: oai.ChatCompletionResponseFormatTypeJSONObject}
		resp, err = l.client.CreateChatCompletion(ctx, req)
	}
	if err != nil {
		return cortex.Article{}, cortex.ArticleUsage{}, fmt.Errorf("openai: write article: %w", err)
	}
	usage := cortex.ArticleUsage{InputTokens: resp.Usage.PromptTokens, OutputTokens: resp.Usage.CompletionTokens}
	if len(resp.Choices) == 0 {
		return cortex.Article{}, usage, fmt.Errorf("openai: no choices in article response: %w", cortex.ErrArticleBadOutput)
	}
	ch := resp.Choices[0]
	if ch.FinishReason == oai.FinishReasonLength {
		return cortex.Article{}, usage, cortex.ErrArticleTruncated
	}
	art, err := cortex.ParseArticle(ch.Message.Content)
	if err != nil {
		return cortex.Article{}, usage, fmt.Errorf("openai: %w", err)
	}
	return art, usage, nil
}

var _ cortex.ArticleWriter = (*LLM)(nil)
