package model

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
	WrittenBy  string         `json:"written_by,omitempty"`
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
	// EvidenceCount is how many times this edge has been asserted; each
	// repeat sighting increments it instead of inserting a duplicate row.
	EvidenceCount int       `json:"evidence_count"`
	LastSeenAt    time.Time `json:"last_seen_at"`
	// ValidAt is when the relationship began (nil = unknown, treated as
	// always); InvalidAt when it ended (nil = still holds). A closed edge
	// stays in the graph for history and as-of reads.
	ValidAt   *time.Time `json:"valid_at,omitempty"`
	InvalidAt *time.Time `json:"invalid_at,omitempty"`
}

type Chunk struct {
	ID        string         `json:"id"`
	EntityID  string         `json:"entity_id,omitempty"`
	Content   string         `json:"content"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	WrittenBy string         `json:"written_by,omitempty"`
}

type Memory struct {
	ID        string   `json:"id"`
	Content   string   `json:"content"`
	EntityIDs []string `json:"entity_ids,omitempty"`
	// About names the extracted entities this memory is about. It is set by
	// extractors and used only during Remember to link the memory to those
	// entities; it is not stored.
	About     []string `json:"about,omitempty"`
	Source    string   `json:"source,omitempty"`
	Speaker   string   `json:"speaker,omitempty"`
	WrittenBy string   `json:"written_by,omitempty"`
	// Static marks an identity/preference fact (vs. an episodic one). Static
	// memories are exempt from confidence decay and surface in a profile's
	// static[] section; non-static memories decay and feed dynamic[]. Set by
	// the LLM extractor; defaults false.
	Static     bool      `json:"static"`
	Confidence float64   `json:"confidence"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	// Bi-temporal fields (Tier 2a). Pointers so SQL NULL is representable.
	// ValidAt/InvalidAt are event-time (when the fact became / stopped being
	// true). ExpiredAt is ingestion-time (when the system retired the memory).
	// All nil on memories written by the standard ingest path.
	ValidAt   *time.Time `json:"valid_at,omitempty"`
	InvalidAt *time.Time `json:"invalid_at,omitempty"`
	ExpiredAt *time.Time `json:"expired_at,omitempty"`
	// ForgetAfter is an ingestion-set expiry: the event time at which this
	// memory should stop being current (e.g. "meeting at 3pm today",
	// "exam tomorrow"). The Maintain expire pass soft-retires memories past
	// this time. nil = no expiry. Set by the LLM extractor, which resolves
	// relative phrases against the ingest date.
	ForgetAfter *time.Time `json:"forget_after,omitempty"`
}

// MemoryEdge is a typed directed edge between two memories (facts-on-facts).
// It reads "SourceID <Type> TargetID" — e.g. a supersedes edge has SourceID =
// the newer memory and TargetID = the stale one it replaces. Edges are
// additive metadata: they record relationships discovered during
// reconciliation/maintenance and never change the memories themselves.
type MemoryEdge struct {
	ID        string    `json:"id"`
	SourceID  string    `json:"source_id"`
	TargetID  string    `json:"target_id"`
	Type      string    `json:"type"`
	Source    string    `json:"source,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Memory edge types. SourceID <type> TargetID:
//   - EdgeSupersedes: the newer memory replaces/contradicts the older (target).
//   - EdgeDerives:    the source memory is derived/inferred from the target.
//   - EdgeExtends:    the source memory adds detail to the target without
//     contradicting it.
const (
	EdgeSupersedes = "supersedes"
	EdgeDerives    = "derives"
	EdgeExtends    = "extends"
)

type Result struct {
	// ID identifies the underlying memory, chunk or entity so a caller can
	// target it (for example with forget).
	ID         string         `json:"id,omitempty"`
	Type       string         `json:"type"`
	Content    string         `json:"content"`
	Score      float64        `json:"score"`
	Confidence float64        `json:"confidence"`
	EntityIDs  []string       `json:"entity_ids,omitempty"`
	Source     string         `json:"source,omitempty"`
	Speaker    string         `json:"speaker,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

// RecallResult wraps a ranked recall with an aggregate strength signal and
// an abstention hint. Strength is a cosine relevance score in [0,1] — the max
// query-result cosine similarity over the top results — when an embedder is
// configured; otherwise it falls back to the confidence-weighted RRF top
// score. It is 0 when empty. Abstain is true when the engine has nothing
// sufficiently relevant — a cue for agents to say "I don't know that"
// rather than fabricate an answer.
type RecallResult struct {
	Results  []Result `json:"results"`
	Strength float64  `json:"strength"`
	Abstain  bool     `json:"abstain"`
}

// AbstainThreshold is the confidence-weighted RRF top-score below which
// RecallWithStrength advises abstention. This is now the FALLBACK signal,
// used only when no embedder is configured (the cosine-relevance path is
// preferred when one is). Tuned conservatively: the goal is to catch
// empty/irrelevant recalls, not to suppress weak-but-real hits. Calibration:
// a single RRF hit scores 1/(k+1) with k=60, i.e. ~0.0164, so 0.005 sits just
// below that floor multiplied by a low confidence — raising it much further
// risks abstaining on every single-hit recall.
const AbstainThreshold = 0.005

// AbstainTopK bounds how many top results contribute to the cosine relevance
// strength used for abstention. Max cosine over these results is the signal:
// one strongly-matching result should defeat abstention.
const AbstainTopK = 5

// AbstainRelevanceThreshold is the max query-result cosine below which
// RecallWithStrength advises abstention when an embedder is configured.
//
// Calibrated 2026-10-22 via a threshold sweep (cortex-eval) over a real
// 1274-memory graph in Postgres, stored as 512-dim halfvec (HNSW), with
// text-embedding-3-small at 512 dims, using 120 answerable questions, 20
// out-of-domain ("easy") negatives, and 20 counterfactual ("hard") negatives.
// The sweep showed a plateau at t in [0.45, 0.55] where easy-negative
// abstention is 100% and the false-abstention rate on real questions is 0.8%
// (one question of 120, constant across the plateau; it was 0% only at
// t <= 0.25, where easy abstention is 15%); 0.50 is the center of that
// plateau. Easy abstention falls to 95% at 0.40 and false abstention rises to
// 2.5% at 0.60. The 512-dim vectors shift the curve up by about 0.05-0.10 from
// the 1536-dim SQLite calibration (0.40). Out-of-domain questions are
// reliably caught here. Counterfactual "hard" negatives (vocabulary-close but
// asking an absent fact) are NOT reliably caught by query-content cosine: they
// retrieve the same nearby memory a real question would, so their max cosine is
// as high as a true hit's; catching them needs an answer-grounded check beyond
// retrieval similarity, which this signal does not attempt.
const AbstainRelevanceThreshold = 0.50

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

type Option func(*Config)

type Config struct {
	LLM       LLM
	Embedder  Embedder
	Extractor Extractor
	Logf      func(format string, args ...any) // optional diagnostic sink; nil = silent

	MigrationTimeout time.Duration // 0 = default (60s)
}

// WithMigrationTimeout bounds how long Open waits for migrations (default 60s).
func WithMigrationTimeout(d time.Duration) Option {
	return func(c *Config) { c.MigrationTimeout = d }
}

type LLM interface {
	Extract(ctx context.Context, text string, prompt string) (ExtractionResult, error)
	Decompose(ctx context.Context, query string) ([]StructuredQuery, error)
	Summarize(ctx context.Context, texts []string) (string, error)
}

// Reconciler is an optional capability a provider may implement to detect
// contradictions among a set of memories. It is intentionally separate from
// LLM so the core interface stays stable and providers opt in. Reconciliation
// (reconcile.go) type-asserts the configured LLM for this interface and
// no-ops gracefully when it is absent.
//
// DetectConflicts receives a set of memories (typically all currently-valid
// memories linked to one entity) and returns pairs where one memory
// supersedes/contradicts another. The implementation only FLAGS candidates;
// the deterministic gate in reconcile.go decides what is actually applied.
type Reconciler interface {
	DetectConflicts(ctx context.Context, memories []Memory) ([]ConflictPair, error)
}

// ConflictPair is a detected contradiction: the memory identified by
// SupersededByID contradicts/replaces the one identified by StaleID.
type ConflictPair struct {
	StaleID        string `json:"stale_id"`
	SupersededByID string `json:"superseded_by_id"`
	Reason         string `json:"reason"`
}

// RelationDetector is an optional capability a provider may implement to
// detect NON-contradicting relations among memories — one memory extending
// or deriving from another. It is separate from LLM (and from Reconciler) so
// the core interface stays stable and providers opt in. BuildMemoryEdges
// (relate.go) type-asserts the configured LLM for this interface and no-ops
// gracefully when it is absent.
//
// DetectRelations receives memories (typically all currently-valid memories
// linked to one entity) and returns proposed derives/extends relations. The
// implementation only PROPOSES; the deterministic gate in relate.go decides
// what is recorded.
type RelationDetector interface {
	DetectRelations(ctx context.Context, memories []Memory) ([]MemoryRelation, error)
}

// MemoryRelation is an LLM-proposed non-contradicting relation: SourceID
// <Type> TargetID, where Type is EdgeDerives or EdgeExtends. Reads
// subject-verb-object: for extends, source adds detail to target; for
// derives, source is inferred from target.
type MemoryRelation struct {
	SourceID string `json:"source_id"`
	TargetID string `json:"target_id"`
	Type     string `json:"type"`
	Reason   string `json:"reason"`
}

// RelationProposal is a gate-passed relation ready to record as an edge.
type RelationProposal struct {
	SourceID      string `json:"source_id"`
	SourceContent string `json:"source_content"`
	TargetID      string `json:"target_id"`
	TargetContent string `json:"target_content"`
	Type          string `json:"type"`
	Reason        string `json:"reason"`
}

// RejectedRelation is an LLM-proposed relation the deterministic gate rejected.
type RejectedRelation struct {
	SourceID string `json:"source_id"`
	TargetID string `json:"target_id"`
	Type     string `json:"type"`
	Reason   string `json:"reason"`
}

// RelationReport summarizes a BuildMemoryEdges run.
type RelationReport struct {
	EntitiesScanned int                `json:"entities_scanned"`
	MemoriesScanned int                `json:"memories_scanned"`
	Proposed        []RelationProposal `json:"proposed"`
	Rejected        []RejectedRelation `json:"rejected"`
	Skipped         bool               `json:"skipped"`
	SkipReason      string             `json:"skip_reason,omitempty"`
	// EntitiesSkipped counts entities whose LLM call failed (after retries)
	// and were skipped; Errors holds the first few error strings.
	EntitiesSkipped int      `json:"entities_skipped,omitempty"`
	Errors          []string `json:"errors,omitempty"`
}

// RelateOption configures a BuildMemoryEdges run.
type RelateOption func(*RelateConfig)

type RelateConfig struct{}

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

type RememberOption func(*RememberConfig)
type RememberConfig struct {
	Source       string
	Speaker      string
	ContentType  string
	MaxChunkSize int // max characters per chunk before splitting (0 = no split)
}

func WithSource(source string) RememberOption {
	return func(c *RememberConfig) { c.Source = source }
}

// WithSpeaker stamps a speaker/provenance label (e.g. "user", "assistant",
// a document name) on every memory ingested in this Remember call, unless
// the extractor already set one. Records who asserted the fact, so
// attribution questions and assistant-statement recall work.
func WithSpeaker(speaker string) RememberOption {
	return func(c *RememberConfig) { c.Speaker = speaker }
}

func WithContentType(ct string) RememberOption {
	return func(c *RememberConfig) { c.ContentType = ct }
}

// WithMaxChunkChars caps each stored chunk to roughly n characters.
// Long content is split on paragraph and sentence boundaries, then on
// hard boundaries if needed. Each split is stored as its own Chunk and
// embedded independently. The default is DefaultMaxChunkChars; pass 0 to
// disable splitting.
func WithMaxChunkChars(n int) RememberOption {
	return func(c *RememberConfig) { c.MaxChunkSize = n }
}

type RecallOption func(*RecallConfig)
type RecallConfig struct {
	Limit          int
	Source         string
	MinConfidence  float64
	AsOf           *time.Time // non-nil → recall as the graph was valid at this time
	IncludeInvalid bool       // true → no validity filter (include retired memories)
	Rerank         bool       // true → post-fusion MMR diversity rerank (default off; see WithRerank)
	NoGraph        bool       // true → skip graph seeds and proximity boost (see WithGraph)
	TrackAccess    bool       // true → count returned memories as accessed (see WithTrackAccess)
}

// RerankLambda is the MMR tradeoff: relevance weight vs. diversity weight.
// 0.7 keeps relevance primary while still demoting near-duplicates.
const RerankLambda = 0.7

func WithLimit(n int) RecallOption {
	return func(c *RecallConfig) { c.Limit = n }
}

func WithSourceFilter(source string) RecallOption {
	return func(c *RecallConfig) { c.Source = source }
}

// WithMinConfidence filters out recall results below the given threshold.
// Default 0.0 (no filtering). Applied as a hard >= threshold after RRF
// merge, before the limit cap.
func WithMinConfidence(c float64) RecallOption {
	return func(cfg *RecallConfig) { cfg.MinConfidence = c }
}

// WithAsOf recalls memories as they were valid at time t (point-in-time
// history). Mutually exclusive with WithIncludeInvalid, which takes
// precedence if both are set.
func WithAsOf(t time.Time) RecallOption {
	return func(c *RecallConfig) { c.AsOf = &t }
}

// WithIncludeInvalid includes retired/invalidated memories in recall
// (no validity filtering). Takes precedence over WithAsOf.
func WithIncludeInvalid() RecallOption {
	return func(c *RecallConfig) { c.IncludeInvalid = true }
}

// WithRerank toggles the post-fusion MMR diversity rerank. It is OFF by
// default: benchmarking showed it costs ~20pts of mid-rank recall on
// fact-recall queries, where corroborating memories are legitimately similar
// and MMR wrongly demotes them as redundant. Enable it for browse/explore
// queries where spreading out near-duplicate hits is more useful than ranking
// the single best fact first. The pure relevance (confidence-weighted fusion)
// order is the default.
func WithRerank(on bool) RecallOption {
	return func(c *RecallConfig) { c.Rerank = on }
}

// WithGraph turns graph-aware recall on or off (default on). When on, the
// entities a query names (by name or alias) seed it: their memories join the
// fusion as one more ranked list, and results linked to entities within two
// hops of a seed are boosted, closer ones more.
func WithGraph(on bool) RecallOption {
	return func(c *RecallConfig) { c.NoGraph = !on }
}

// WithTrackAccess records the memories this recall returns as accessed
// (access_count, last_accessed_at). Use it where results reach an agent or a
// person, not for benchmarks: access lifts ranking slightly and restarts a
// memory's decay clock.
func WithTrackAccess() RecallOption {
	return func(c *RecallConfig) { c.TrackAccess = true }
}

type RelFilter func(*RelFilterConfig)
type RelFilterConfig struct {
	RelType        string
	AsOf           *time.Time
	IncludeInvalid bool
}

func RelTypeFilter(t string) RelFilter {
	return func(c *RelFilterConfig) { c.RelType = t }
}

// RelAsOf returns the relationships as they held at t instead of now.
func RelAsOf(t time.Time) RelFilter {
	return func(c *RelFilterConfig) { c.AsOf = &t }
}

// RelIncludeInvalid returns ended relationships as well as current ones.
func RelIncludeInvalid() RelFilter {
	return func(c *RelFilterConfig) { c.IncludeInvalid = true }
}

type TraverseOption func(*TraverseConfig)
type TraverseConfig struct {
	Depth      int
	EdgeTypes  []string
	RelFilters []RelFilter
}

// WithTraverseAsOf walks the relationships as they held at t.
func WithTraverseAsOf(t time.Time) TraverseOption {
	return func(c *TraverseConfig) { c.RelFilters = append(c.RelFilters, RelAsOf(t)) }
}

// WithTraverseIncludeInvalid walks ended relationships as well.
func WithTraverseIncludeInvalid() TraverseOption {
	return func(c *TraverseConfig) { c.RelFilters = append(c.RelFilters, RelIncludeInvalid()) }
}

func WithDepth(d int) TraverseOption {
	return func(c *TraverseConfig) { c.Depth = d }
}

func WithEdgeTypes(types ...string) TraverseOption {
	return func(c *TraverseConfig) { c.EdgeTypes = types }
}

func WithLLM(l LLM) Option {
	return func(c *Config) { c.LLM = l }
}

func WithEmbedder(e Embedder) Option {
	return func(c *Config) { c.Embedder = e }
}

func WithExtractor(e Extractor) Option {
	return func(c *Config) { c.Extractor = e }
}

// WithLogger installs an optional diagnostic sink. Cortex uses it to surface
// best-effort failures it would otherwise swallow — most importantly embedder
// errors during recall, which would otherwise make a dead embedding endpoint
// (e.g. an over-quota API key) look indistinguishable from "no vector
// results". The default is nil (silent), preserving prior behavior. Pass e.g.
// WithLogger(log.Printf) or a custom func.
func WithLogger(logf func(format string, args ...any)) Option {
	return func(c *Config) { c.Logf = logf }
}

// CoerceConfidence enforces the [0, 1] invariant for confidence values.
// Zero (the Go zero value, indistinguishable from "unset") is coerced to
// 1.0 — this is what preserves backward compatibility: callers that did
// not specify confidence (deterministic extractor, manual API use, legacy
// tests) get the pre-feature behavior of "treat all data as fully
// confident." Out-of-range values are clamped silently rather than
// errored: failing an entire ingest because of one bad number from an LLM
// is worse UX than clamping and continuing.
func CoerceConfidence(c float64) float64 {
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
	Aliases       int // aliases re-targeted
	// BatchID identifies the soft-delete batch; RestoreBatch undoes the merge.
	BatchID string `json:"batch_id,omitempty"`
}

// Supersession is a gate-passed proposed invalidation produced by a reconcile
// dry-run: StaleID will be soft-invalidated with InvalidAt, because the newer
// SupersededByID memory contradicts it.
type Supersession struct {
	StaleID             string    `json:"stale_id"`
	StaleContent        string    `json:"stale_content"`
	SupersededByID      string    `json:"superseded_by_id"`
	SupersededByContent string    `json:"superseded_by_content"`
	Reason              string    `json:"reason"`
	InvalidAt           time.Time `json:"invalid_at"`
}

// RejectedPair is an LLM-flagged contradiction that the deterministic gate
// rejected (e.g. wrong supersession direction, or an id not in the current
// candidate set).
type RejectedPair struct {
	StaleID        string `json:"stale_id"`
	SupersededByID string `json:"superseded_by_id"`
	Reason         string `json:"reason"` // why the gate rejected it
}

// ReconcileReport summarizes a reconcile dry-run.
type ReconcileReport struct {
	EntitiesScanned int            `json:"entities_scanned"`
	MemoriesScanned int            `json:"memories_scanned"`
	Proposed        []Supersession `json:"proposed"`
	Rejected        []RejectedPair `json:"rejected"`
	Skipped         bool           `json:"skipped"` // true when no Reconciler-capable LLM is configured
	SkipReason      string         `json:"skip_reason,omitempty"`
	// EntitiesSkipped counts entities whose LLM call failed (after retries)
	// and were skipped; Errors holds the first few error strings.
	EntitiesSkipped int      `json:"entities_skipped,omitempty"`
	Errors          []string `json:"errors,omitempty"`
}

// ReconcileOption configures a Reconcile run.
type ReconcileOption func(*ReconcileConfig)

type ReconcileConfig struct{}

// MergeRecord is one entry in an entity's `merged_from` attribute array.
// It snapshots the dropped entity so a merge is recoverable in principle.
type MergeRecord struct {
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

// DuplicatePair is one pair of entities that share a (normalised) type and
// whose names are equal ignoring case or similar enough (Score, from
// NameSimilarity) that they may be the same thing.
type DuplicatePair struct {
	Type  string
	A     EntityRef
	B     EntityRef
	Score float64
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
type LintOption func(*LintConfig)

type LintConfig struct {
	LowConfidence          bool
	LowConfidenceThreshold float64
}

// WithLowConfidence enables the low-confidence memories section
// (skipped by default).
func WithLowConfidence() LintOption {
	return func(c *LintConfig) { c.LowConfidence = true }
}

// WithLowConfidenceThreshold sets the cutoff for "low confidence"
// (default 0.3) and implicitly enables the section.
func WithLowConfidenceThreshold(t float64) LintOption {
	return func(c *LintConfig) {
		c.LowConfidence = true
		c.LowConfidenceThreshold = t
	}
}

type DecayChange struct {
	ID            string  `json:"id"`
	Content       string  `json:"content"`
	OldConfidence float64 `json:"old_confidence"`
	NewConfidence float64 `json:"new_confidence"`
	Pruned        bool    `json:"pruned"` // true = auto-soft-invalidated (below floor)
}

type DecayReport struct {
	Scanned int           `json:"scanned"`
	Decayed int           `json:"decayed"` // confidence changed
	Pruned  int           `json:"pruned"`  // soft-invalidated below floor
	DryRun  bool          `json:"dry_run"`
	Changes []DecayChange `json:"changes"`
}

type DecayOption func(*DecayConfig)

type DecayConfig struct {
	HalfLife time.Duration
	Floor    float64
	DryRun   bool
}

// WithHalfLife sets the decay half-life (default 90 days).
func WithHalfLife(d time.Duration) DecayOption { return func(c *DecayConfig) { c.HalfLife = d } }

// WithFloor sets the confidence floor below which a memory is auto-pruned
// (default 0.05).
func WithFloor(f float64) DecayOption { return func(c *DecayConfig) { c.Floor = f } }

// WithDecayDryRun computes the decay report without writing anything.
func WithDecayDryRun() DecayOption { return func(c *DecayConfig) { c.DryRun = true } }

// MaintainReport summarizes a Maintain reconsolidation pass — the collected
// sub-reports from reconcile, relate, and decay. A nil sub-report means that
// pass was skipped via its WithoutX toggle.
type MaintainReport struct {
	DryRun    bool             `json:"dry_run"`
	Reconcile *ReconcileReport `json:"reconcile,omitempty"`
	Relate    *RelationReport  `json:"relate,omitempty"`
	Decay     *DecayReport     `json:"decay,omitempty"`
	Expire    *ExpireReport    `json:"expire,omitempty"`
	Profile   *ProfileReport   `json:"profile,omitempty"`
}

// SkippedItems counts the items (entities, profiles) the passes skipped
// because of provider errors, so a caller can tell a clean run from a
// degraded one.
func (r MaintainReport) SkippedItems() int {
	n := 0
	if r.Reconcile != nil {
		n += r.Reconcile.EntitiesSkipped
	}
	if r.Relate != nil {
		n += r.Relate.EntitiesSkipped
	}
	if r.Profile != nil {
		n += r.Profile.Undistilled
	}
	return n
}

type MaintainOption func(*MaintainConfig)

type MaintainConfig struct {
	DryRun        bool
	SkipReconcile bool
	SkipRelate    bool
	SkipDecay     bool
	SkipExpire    bool
	SkipProfile   bool
	DecayOpts     []DecayOption
}

// WithMaintainDryRun runs Maintain without writing anything: reconcile uses its
// dry-run path, relate is skipped (it has no dry-run mode), and decay uses
// WithDecayDryRun.
func WithMaintainDryRun() MaintainOption { return func(c *MaintainConfig) { c.DryRun = true } }

// WithoutReconcile skips the reconcile pass.
func WithoutReconcile() MaintainOption { return func(c *MaintainConfig) { c.SkipReconcile = true } }

// WithoutRelate skips the relate pass.
func WithoutRelate() MaintainOption { return func(c *MaintainConfig) { c.SkipRelate = true } }

// WithoutDecay skips the decay pass.
func WithoutDecay() MaintainOption { return func(c *MaintainConfig) { c.SkipDecay = true } }

// WithMaintainDecayOptions forwards decay options to the decay pass.
func WithMaintainDecayOptions(o ...DecayOption) MaintainOption {
	return func(c *MaintainConfig) { c.DecayOpts = o }
}

// WithoutProfile skips the profile-refresh pass in Maintain.
func WithoutProfile() MaintainOption { return func(c *MaintainConfig) { c.SkipProfile = true } }

// WithoutExpire skips the expire pass in Maintain.
func WithoutExpire() MaintainOption { return func(c *MaintainConfig) { c.SkipExpire = true } }

// --- Profile ---

// Profile is an entity's cached context digest: a stable "who they are"
// (Static) plus recent context (Dynamic). It is built from the entity's
// currently-valid linked memories and cached on the entity. Distilled is
// false when no LLM was available and the lists are raw memory texts.
// Cached is true when served from the cache without a rebuild this call.
type Profile struct {
	EntityID  string    `json:"entity_id"`
	Name      string    `json:"name"`
	Static    []string  `json:"static"`
	Dynamic   []string  `json:"dynamic"`
	BuiltAt   time.Time `json:"built_at"`
	Distilled bool      `json:"distilled"`
	Cached    bool      `json:"cached"`
}

// ProfileReport summarizes a RefreshProfiles run (the Maintain profile pass).
type ProfileReport struct {
	Scanned int      `json:"scanned"`
	Rebuilt int      `json:"rebuilt"`
	Skipped []string `json:"skipped,omitempty"` // entity IDs whose build failed
	// Undistilled counts profiles built from raw memories because the LLM
	// distill failed (after retries); Errors holds the first few error strings.
	Undistilled int      `json:"undistilled,omitempty"`
	Errors      []string `json:"errors,omitempty"`
}

// Profile tuning defaults.
const (
	ProfileDefaultTTL       = 24 * time.Hour
	ProfileDefaultRecentK   = 7
	ProfileDefaultWindow    = 30 * 24 * time.Hour
	ProfileDefaultStaticCap = 60 // max static candidates handed to the distiller
)

type ProfileOption func(*ProfileConfig)

type ProfileConfig struct {
	TTL       time.Duration
	RecentK   int
	Window    time.Duration
	StaticCap int
}

func DefaultProfileConfig() ProfileConfig {
	return ProfileConfig{
		TTL:       ProfileDefaultTTL,
		RecentK:   ProfileDefaultRecentK,
		Window:    ProfileDefaultWindow,
		StaticCap: ProfileDefaultStaticCap,
	}
}

// WithProfileTTL sets how long a cached profile is served before a rebuild
// (default 24h).
func WithProfileTTL(d time.Duration) ProfileOption {
	return func(c *ProfileConfig) { c.TTL = d }
}

// WithProfileRecentK sets how many recent memories form the dynamic section
// (default 7).
func WithProfileRecentK(n int) ProfileOption {
	return func(c *ProfileConfig) { c.RecentK = n }
}

// WithProfileWindow sets the recency window for dynamic memories (default 30d).
func WithProfileWindow(d time.Duration) ProfileOption {
	return func(c *ProfileConfig) { c.Window = d }
}

// WithProfileStaticCap caps how many memories feed the static section
// (default 60).
func WithProfileStaticCap(n int) ProfileOption {
	return func(c *ProfileConfig) { c.StaticCap = n }
}

// --- Expire (TTL) ---

// ExpireChange is one memory retired (or, under dry-run, that would be
// retired) by the expire pass because its ForgetAfter has passed.
type ExpireChange struct {
	ID          string    `json:"id"`
	Content     string    `json:"content"`
	ForgetAfter time.Time `json:"forget_after"`
}

// ExpireReport summarizes an ExpireMemories run.
type ExpireReport struct {
	Scanned int            `json:"scanned"`
	Expired int            `json:"expired"`
	DryRun  bool           `json:"dry_run"`
	Changes []ExpireChange `json:"changes"`
}

type ExpireOption func(*ExpireConfig)

type ExpireConfig struct {
	DryRun bool
}

// WithExpireDryRun computes the expire report without writing anything.
func WithExpireDryRun() ExpireOption { return func(c *ExpireConfig) { c.DryRun = true } }
