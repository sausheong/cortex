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
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	EntityIDs []string  `json:"entity_ids,omitempty"`
	Source    string    `json:"source,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Result struct {
	Type      string         `json:"type"`
	Content   string         `json:"content"`
	Score     float64        `json:"score"`
	EntityIDs []string       `json:"entity_ids,omitempty"`
	Source    string         `json:"source,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
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
	limit  int
	source string
}

func WithLimit(n int) RecallOption {
	return func(c *recallConfig) { c.limit = n }
}

func WithSourceFilter(source string) RecallOption {
	return func(c *recallConfig) { c.source = source }
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
