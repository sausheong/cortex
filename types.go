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
	Speaker    string    `json:"speaker,omitempty"`
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
// Calibrated via a threshold sweep (cortex-eval) over a real 1202-memory graph
// with text-embedding-3-small, using 120 answerable questions, 20 out-of-domain
// ("easy") negatives, and 20 counterfactual ("hard") negatives. The sweep showed
// a clean plateau at t ∈ [0.35, 0.45] where easy-negative abstention is 100% and
// the false-abstention rate on real questions is 0%; 0.40 is the center of that
// plateau (false abstention first appears at 0.50). Out-of-domain questions are
// reliably caught here. Counterfactual "hard" negatives (vocabulary-close but
// asking an absent fact) are NOT reliably caught by query-content cosine — they
// retrieve the same nearby memory a real question would, so their max cosine is
// as high as a true hit's; catching them needs an answer-grounded check beyond
// retrieval similarity, which this signal does not attempt.
const AbstainRelevanceThreshold = 0.40

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
	logf      func(format string, args ...any) // optional diagnostic sink; nil = silent
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
}

// RelateOption configures a BuildMemoryEdges run.
type RelateOption func(*relateConfig)

type relateConfig struct{}

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
	speaker      string
	contentType  string
	maxChunkSize int // max characters per chunk before splitting (0 = no split)
}

func WithSource(source string) RememberOption {
	return func(c *rememberConfig) { c.source = source }
}

// WithSpeaker stamps a speaker/provenance label (e.g. "user", "assistant",
// a document name) on every memory ingested in this Remember call, unless
// the extractor already set one. Records who asserted the fact, so
// attribution questions and assistant-statement recall work.
func WithSpeaker(speaker string) RememberOption {
	return func(c *rememberConfig) { c.speaker = speaker }
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
	limit          int
	source         string
	minConfidence  float64
	asOf           *time.Time // non-nil → recall as the graph was valid at this time
	includeInvalid bool       // true → no validity filter (include retired memories)
	rerank         bool       // true → post-fusion MMR diversity rerank (default off; see WithRerank)
}

// RerankLambda is the MMR tradeoff: relevance weight vs. diversity weight.
// 0.7 keeps relevance primary while still demoting near-duplicates.
const RerankLambda = 0.7

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

// WithAsOf recalls memories as they were valid at time t (point-in-time
// history). Mutually exclusive with WithIncludeInvalid, which takes
// precedence if both are set.
func WithAsOf(t time.Time) RecallOption {
	return func(c *recallConfig) { c.asOf = &t }
}

// WithIncludeInvalid includes retired/invalidated memories in recall
// (no validity filtering). Takes precedence over WithAsOf.
func WithIncludeInvalid() RecallOption {
	return func(c *recallConfig) { c.includeInvalid = true }
}

// WithRerank toggles the post-fusion MMR diversity rerank. It is OFF by
// default: benchmarking showed it costs ~20pts of mid-rank recall on
// fact-recall queries, where corroborating memories are legitimately similar
// and MMR wrongly demotes them as redundant. Enable it for browse/explore
// queries where spreading out near-duplicate hits is more useful than ranking
// the single best fact first. The pure relevance (confidence-weighted fusion)
// order is the default.
func WithRerank(on bool) RecallOption {
	return func(c *recallConfig) { c.rerank = on }
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

// WithLogger installs an optional diagnostic sink. Cortex uses it to surface
// best-effort failures it would otherwise swallow — most importantly embedder
// errors during recall, which would otherwise make a dead embedding endpoint
// (e.g. an over-quota API key) look indistinguishable from "no vector
// results". The default is nil (silent), preserving prior behavior. Pass e.g.
// WithLogger(log.Printf) or a custom func.
func WithLogger(logf func(format string, args ...any)) Option {
	return func(c *config) { c.logf = logf }
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
}

// ReconcileOption configures a Reconcile run.
type ReconcileOption func(*reconcileConfig)

type reconcileConfig struct{}

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

type DecayOption func(*decayConfig)

type decayConfig struct {
	halfLife time.Duration
	floor    float64
	dryRun   bool
}

// WithHalfLife sets the decay half-life (default 90 days).
func WithHalfLife(d time.Duration) DecayOption { return func(c *decayConfig) { c.halfLife = d } }

// WithFloor sets the confidence floor below which a memory is auto-pruned
// (default 0.05).
func WithFloor(f float64) DecayOption { return func(c *decayConfig) { c.floor = f } }

// WithDecayDryRun computes the decay report without writing anything.
func WithDecayDryRun() DecayOption { return func(c *decayConfig) { c.dryRun = true } }

// MaintainReport summarizes a Maintain reconsolidation pass — the collected
// sub-reports from reconcile, relate, and decay. A nil sub-report means that
// pass was skipped via its WithoutX toggle.
type MaintainReport struct {
	DryRun    bool             `json:"dry_run"`
	Reconcile *ReconcileReport `json:"reconcile,omitempty"`
	Relate    *RelationReport  `json:"relate,omitempty"`
	Decay     *DecayReport     `json:"decay,omitempty"`
	Profile   *ProfileReport   `json:"profile,omitempty"`
}

type MaintainOption func(*maintainConfig)

type maintainConfig struct {
	dryRun        bool
	skipReconcile bool
	skipRelate    bool
	skipDecay     bool
	skipProfile   bool
	decayOpts     []DecayOption
}

// WithMaintainDryRun runs Maintain without writing anything: reconcile uses its
// dry-run path, relate is skipped (it has no dry-run mode), and decay uses
// WithDecayDryRun.
func WithMaintainDryRun() MaintainOption { return func(c *maintainConfig) { c.dryRun = true } }

// WithoutReconcile skips the reconcile pass.
func WithoutReconcile() MaintainOption { return func(c *maintainConfig) { c.skipReconcile = true } }

// WithoutRelate skips the relate pass.
func WithoutRelate() MaintainOption { return func(c *maintainConfig) { c.skipRelate = true } }

// WithoutDecay skips the decay pass.
func WithoutDecay() MaintainOption { return func(c *maintainConfig) { c.skipDecay = true } }

// WithMaintainDecayOptions forwards decay options to the decay pass.
func WithMaintainDecayOptions(o ...DecayOption) MaintainOption {
	return func(c *maintainConfig) { c.decayOpts = o }
}

// WithoutProfile skips the profile-refresh pass in Maintain.
func WithoutProfile() MaintainOption { return func(c *maintainConfig) { c.skipProfile = true } }

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
}

// Profile tuning defaults.
const (
	ProfileDefaultTTL       = 24 * time.Hour
	ProfileDefaultRecentK   = 7
	ProfileDefaultWindow    = 30 * 24 * time.Hour
	ProfileDefaultStaticCap = 15
)

type ProfileOption func(*profileConfig)

type profileConfig struct {
	ttl       time.Duration
	recentK   int
	window    time.Duration
	staticCap int
}

func defaultProfileConfig() profileConfig {
	return profileConfig{
		ttl:       ProfileDefaultTTL,
		recentK:   ProfileDefaultRecentK,
		window:    ProfileDefaultWindow,
		staticCap: ProfileDefaultStaticCap,
	}
}

// WithProfileTTL sets how long a cached profile is served before a rebuild
// (default 24h).
func WithProfileTTL(d time.Duration) ProfileOption {
	return func(c *profileConfig) { c.ttl = d }
}

// WithProfileRecentK sets how many recent memories form the dynamic section
// (default 7).
func WithProfileRecentK(n int) ProfileOption {
	return func(c *profileConfig) { c.recentK = n }
}

// WithProfileWindow sets the recency window for dynamic memories (default 30d).
func WithProfileWindow(d time.Duration) ProfileOption {
	return func(c *profileConfig) { c.window = d }
}

// WithProfileStaticCap caps how many memories feed the static section
// (default 15).
func WithProfileStaticCap(n int) ProfileOption {
	return func(c *profileConfig) { c.staticCap = n }
}
