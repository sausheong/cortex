package cortex

import (
	"context"
	"math"
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

// RecallResult wraps a ranked recall with an aggregate strength signal and
// an abstention hint. Strength is the confidence-weighted score of the top
// result (0 when empty). Abstain is true when the engine has nothing
// sufficiently relevant — a cue for agents to say "I don't know that"
// rather than fabricate an answer.
type RecallResult struct {
	Results  []Result `json:"results"`
	Strength float64  `json:"strength"`
	Abstain  bool     `json:"abstain"`
}

// AbstainThreshold is the confidence-weighted top-score below which
// RecallWithStrength advises abstention. Tuned conservatively: the goal is
// to catch empty/irrelevant recalls, not to suppress weak-but-real hits.
const AbstainThreshold = 0.005

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
	if math.IsNaN(c) || c == 0 {
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

// MergeStats reports what MergeEntities did (or would do, under dry-run).
type MergeStats struct {
	Relationships int // re-targeted (after dedup)
	Memories      int // memory_entities rows re-targeted
	Chunks        int // re-targeted
	Embeddings    int // dropped (stale embedding for drop entity)
	DupesDropped  int // duplicate relationships + memory_entity rows removed during dedup
	AttrConflicts int // count of attributes where keep already had a value (keep won)
}

// mergeRecord is one entry in an entity's `merged_from` attribute array.
// It snapshots the dropped entity so a merge is recoverable in principle.
type mergeRecord struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Type     string         `json:"type"`
	Source   string         `json:"source,omitempty"`
	Attrs    map[string]any `json:"attrs,omitempty"`
	MergedAt time.Time      `json:"merged_at"`
}

// --- Lint ---

// LintReport summarizes the cleanup candidates the lint scan found.
type LintReport struct {
	EntityCount       int
	RelationshipCount int
	MemoryCount       int

	Orphans               []EntityRef
	EntitiesNoMemories    []EntityRef
	NearDuplicates        []DuplicatePair
	DeadSources           []string
	UnlinkedMemories      []MemoryRef
	LowConfidenceMemories []MemoryRef // populated only when WithLowConfidence is set
}

// EntityRef is a minimal entity descriptor for lint findings.
type EntityRef struct {
	ID   string
	Name string
	Type string
}

// DuplicatePair is one pair of entities that share a type and have
// case-insensitively-equal names.
type DuplicatePair struct {
	Type string
	A    EntityRef
	B    EntityRef
}

// MemoryRef is a minimal memory descriptor for lint findings.
// Content is truncated to ~80 chars + "..." if longer.
type MemoryRef struct {
	ID         string
	Content    string
	Source     string
	Confidence float64
}

// LintOption configures Lint behavior.
type LintOption func(*lintConfig)

type lintConfig struct {
	lowConfidence          bool
	lowConfidenceThreshold float64
}

// WithLowConfidence enables the low-confidence memories section
// (skipped by default).
func WithLowConfidence() LintOption {
	return func(c *lintConfig) { c.lowConfidence = true }
}

// WithLowConfidenceThreshold sets the cutoff for "low confidence"
// (default 0.3) and implicitly enables the section.
func WithLowConfidenceThreshold(t float64) LintOption {
	return func(c *lintConfig) {
		c.lowConfidence = true
		c.lowConfidenceThreshold = t
	}
}
