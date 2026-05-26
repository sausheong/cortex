package cortex

import (
	"context"
	"time"
)

type Entity struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Name       string         `json:"name"`
	Attributes map[string]any `json:"attributes,omitempty"`
	Source     string         `json:"source,omitempty"`
	Confidence float64        `json:"confidence"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

type Relationship struct {
	ID         string         `json:"id"`
	SourceID   string         `json:"source_id"`
	TargetID   string         `json:"target_id"`
	Type       string         `json:"type"`
	Attributes map[string]any `json:"attributes,omitempty"`
	Source     string         `json:"source,omitempty"`
	Confidence float64        `json:"confidence"`
	CreatedAt  time.Time      `json:"created_at"`
}

type Chunk struct {
	ID        string         `json:"id"`
	EntityID  string         `json:"entity_id,omitempty"`
	Content   string         `json:"content"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

type Memory struct {
	ID         string    `json:"id"`
	Content    string    `json:"content"`
	EntityIDs  []string  `json:"entity_ids,omitempty"`
	Source     string    `json:"source,omitempty"`
	Confidence float64   `json:"confidence"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Result struct {
	Type       string         `json:"type"`
	Content    string         `json:"content"`
	Score      float64        `json:"score"`
	Confidence float64        `json:"confidence"`
	EntityIDs  []string       `json:"entity_ids,omitempty"`
	Source     string         `json:"source,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

type Filter struct {
	EntityID string
	Source   string
	Type     string
}

type EntityFilter struct {
	Type     string
	NameLike string
	Source   string
}

type Graph struct {
	Entities      []Entity       `json:"entities"`
	Relationships []Relationship `json:"relationships"`
}

type Option func(*config)

type config struct {
	llm       LLM
	embedder  Embedder
	extractor Extractor
}

type LLM interface {
	Extract(ctx context.Context, text string, prompt string) (ExtractionResult, error)
	Decompose(ctx context.Context, query string) ([]StructuredQuery, error)
	Summarize(ctx context.Context, texts []string) (string, error)
}

type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Dimensions() int
}

type Extractor interface {
	Extract(ctx context.Context, content string, contentType string) (*Extraction, error)
}

type Extraction struct {
	Entities      []Entity
	Relationships []Relationship
	Memories      []Memory
}

type ExtractionResult struct {
	Raw    string
	Parsed *Extraction
}

type StructuredQuery struct {
	Type   string         `json:"type"`
	Params map[string]any `json:"params"`
}

type RememberOption func(*rememberConfig)
type rememberConfig struct {
	source       string
	contentType  string
	maxChunkSize int // max characters per chunk before splitting (0 = no split)
}

func WithSource(source string) RememberOption {
	return func(c *rememberConfig) { c.source = source }
}

func WithContentType(ct string) RememberOption {
	return func(c *rememberConfig) { c.contentType = ct }
}

// WithMaxChunkChars caps each stored chunk to roughly n characters.
// Long content is split on paragraph and sentence boundaries, then on
// hard boundaries if needed. Each split is stored as its own Chunk and
// embedded independently. Pass 0 to disable splitting (default).
//
// A safe default for typical embedding models (nomic-embed-text, bge-*,
// OpenAI text-embedding-3-*) is 6000 chars (~1500 tokens).
func WithMaxChunkChars(n int) RememberOption {
	return func(c *rememberConfig) { c.maxChunkSize = n }
}

type RecallOption func(*recallConfig)
type recallConfig struct {
	limit         int
	source        string
	minConfidence float64
}

func WithLimit(n int) RecallOption {
	return func(c *recallConfig) { c.limit = n }
}

func WithSourceFilter(source string) RecallOption {
	return func(c *recallConfig) { c.source = source }
}

// WithMinConfidence filters out recall results below the given threshold.
// Default 0.0 (no filtering). Applied as a hard >= threshold after RRF
// merge, before the limit cap.
func WithMinConfidence(c float64) RecallOption {
	return func(cfg *recallConfig) { cfg.minConfidence = c }
}

type RelFilter func(*relFilterConfig)
type relFilterConfig struct {
	relType string
}

func RelTypeFilter(t string) RelFilter {
	return func(c *relFilterConfig) { c.relType = t }
}

type TraverseOption func(*traverseConfig)
type traverseConfig struct {
	depth     int
	edgeTypes []string
}

func WithDepth(d int) TraverseOption {
	return func(c *traverseConfig) { c.depth = d }
}

func WithEdgeTypes(types ...string) TraverseOption {
	return func(c *traverseConfig) { c.edgeTypes = types }
}

func WithLLM(l LLM) Option {
	return func(c *config) { c.llm = l }
}

func WithEmbedder(e Embedder) Option {
	return func(c *config) { c.embedder = e }
}

func WithExtractor(e Extractor) Option {
	return func(c *config) { c.extractor = e }
}

// coerceConfidence enforces the [0, 1] invariant for confidence values.
// Zero (the Go zero value, indistinguishable from "unset") is coerced to
// 1.0 — this is what preserves backward compatibility: callers that did
// not specify confidence (deterministic extractor, manual API use, legacy
// tests) get the pre-feature behavior of "treat all data as fully
// confident." Out-of-range values are clamped silently rather than
// errored: failing an entire ingest because of one bad number from an LLM
// is worse UX than clamping and continuing.
func coerceConfidence(c float64) float64 {
	if c == 0 {
		return 1.0
	}
	if c < 0 {
		return 0
	}
	if c > 1 {
		return 1
	}
	return c
}
