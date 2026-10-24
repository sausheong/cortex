package cortex

import (
	"time"

	"github.com/sausheong/cortex/internal/model"
)

// Exported functions that forward to the model package: option constructors
// and the pure parsers.

// ParseEventTime turns a natural-language or ISO date string into a UTC
// *time.Time, or nil when the string is empty or unparseable. It is the
// lenient front-door for LLM-supplied event times (memory valid_at): the
// extractor may emit "March 2026", "2026-03", an RFC3339 stamp, or nothing
// at all, and any failure degrades to nil (NULL in storage) rather than
// failing the ingest.
func ParseEventTime(s string) *time.Time { return model.ParseEventTime(s) }

// ParseExtractionJSON parses an LLM's extraction output into an Extraction.
// Markdown code fences are stripped first, since some upstreams wrap JSON in
// them despite a JSON-only prompt. Relationship sources and targets are
// entity names (Remember resolves them to IDs); each memory's "about" names
// the entities it concerns. A memory given as a bare string is accepted.
func ParseExtractionJSON(raw string) (*Extraction, error) { return model.ParseExtractionJSON(raw) }

// RelAsOf returns the relationships as they held at t instead of now.
func RelAsOf(t time.Time) RelFilter { return model.RelAsOf(t) }

// RelIncludeInvalid returns ended relationships as well as current ones.
func RelIncludeInvalid() RelFilter { return model.RelIncludeInvalid() }

func RelTypeFilter(t string) RelFilter { return model.RelTypeFilter(t) }

// WithAsOf recalls memories as they were valid at time t (point-in-time
// history). Mutually exclusive with WithIncludeInvalid, which takes
// precedence if both are set.
func WithAsOf(t time.Time) RecallOption { return model.WithAsOf(t) }

func WithContentType(ct string) RememberOption { return model.WithContentType(ct) }

// WithDecayDryRun computes the decay report without writing anything.
func WithDecayDryRun() DecayOption { return model.WithDecayDryRun() }

func WithDepth(d int) TraverseOption { return model.WithDepth(d) }

func WithEdgeTypes(types ...string) TraverseOption { return model.WithEdgeTypes(types...) }

func WithEmbedder(e Embedder) Option { return model.WithEmbedder(e) }

// WithExpireDryRun computes the expire report without writing anything.
func WithExpireDryRun() ExpireOption { return model.WithExpireDryRun() }

func WithExtractor(e Extractor) Option { return model.WithExtractor(e) }

// WithFloor sets the confidence floor below which a memory is auto-pruned
// (default 0.05).
func WithFloor(f float64) DecayOption { return model.WithFloor(f) }

// WithGraph turns graph-aware recall on or off (default on). When on, the
// entities a query names (by name or alias) seed it: their memories join the
// fusion as one more ranked list, and results linked to entities within two
// hops of a seed are boosted, closer ones more.
func WithGraph(on bool) RecallOption { return model.WithGraph(on) }

// WithHalfLife sets the decay half-life (default 90 days).
func WithHalfLife(d time.Duration) DecayOption { return model.WithHalfLife(d) }

// WithIncludeInvalid includes retired/invalidated memories in recall
// (no validity filtering). Takes precedence over WithAsOf.
func WithIncludeInvalid() RecallOption { return model.WithIncludeInvalid() }

func WithLLM(l LLM) Option { return model.WithLLM(l) }

func WithLimit(n int) RecallOption { return model.WithLimit(n) }

// WithLogger installs an optional diagnostic sink. Cortex uses it to surface
// best-effort failures it would otherwise swallow — most importantly embedder
// errors during recall, which would otherwise make a dead embedding endpoint
// (e.g. an over-quota API key) look indistinguishable from "no vector
// results". The default is nil (silent), preserving prior behavior. Pass e.g.
// WithLogger(log.Printf) or a custom func.
func WithLogger(logf func(format string, args ...any)) Option { return model.WithLogger(logf) }

// WithLowConfidence enables the low-confidence memories section
// (skipped by default).
func WithLowConfidence() LintOption { return model.WithLowConfidence() }

// WithLowConfidenceThreshold sets the cutoff for "low confidence"
// (default 0.3) and implicitly enables the section.
func WithLowConfidenceThreshold(t float64) LintOption { return model.WithLowConfidenceThreshold(t) }

// WithMaintainDecayOptions forwards decay options to the decay pass.
func WithMaintainDecayOptions(o ...DecayOption) MaintainOption {
	return model.WithMaintainDecayOptions(o...)
}

// WithMaintainDryRun runs Maintain without writing anything: reconcile uses its
// dry-run path, relate is skipped (it has no dry-run mode), and decay uses
// WithDecayDryRun.
func WithMaintainDryRun() MaintainOption { return model.WithMaintainDryRun() }

// WithMaxChunkChars caps each stored chunk to roughly n characters.
// Long content is split on paragraph and sentence boundaries, then on
// hard boundaries if needed. Each split is stored as its own Chunk and
// embedded independently. The default is DefaultMaxChunkChars; pass 0 to
// disable splitting.
func WithMaxChunkChars(n int) RememberOption { return model.WithMaxChunkChars(n) }

// WithMigrationTimeout bounds how long Open waits for migrations (default 60s).
func WithMigrationTimeout(d time.Duration) Option { return model.WithMigrationTimeout(d) }

// WithMinConfidence filters out recall results below the given threshold.
// Default 0.0 (no filtering). Applied as a hard >= threshold after RRF
// merge, before the limit cap.
func WithMinConfidence(c float64) RecallOption { return model.WithMinConfidence(c) }

// WithNormalizeDryRun reports what NormalizeGraph would change without
// writing anything.
func WithNormalizeDryRun() NormalizeOption { return model.WithNormalizeDryRun() }

// WithProfileRecentK sets how many recent memories form the dynamic section
// (default 7).
func WithProfileRecentK(n int) ProfileOption { return model.WithProfileRecentK(n) }

// WithProfileStaticCap caps how many memories feed the static section
// (default 60).
func WithProfileStaticCap(n int) ProfileOption { return model.WithProfileStaticCap(n) }

// WithProfileTTL sets how long a cached profile is served before a rebuild
// (default 24h).
func WithProfileTTL(d time.Duration) ProfileOption { return model.WithProfileTTL(d) }

// WithProfileWindow sets the recency window for dynamic memories (default 30d).
func WithProfileWindow(d time.Duration) ProfileOption { return model.WithProfileWindow(d) }

// WithRerank toggles the post-fusion MMR diversity rerank. It is OFF by
// default: benchmarking showed it costs ~20pts of mid-rank recall on
// fact-recall queries, where corroborating memories are legitimately similar
// and MMR wrongly demotes them as redundant. Enable it for browse/explore
// queries where spreading out near-duplicate hits is more useful than ranking
// the single best fact first. The pure relevance (confidence-weighted fusion)
// order is the default.
func WithRerank(on bool) RecallOption { return model.WithRerank(on) }

func WithSource(source string) RememberOption { return model.WithSource(source) }

func WithSourceFilter(source string) RecallOption { return model.WithSourceFilter(source) }

// WithSpeaker stamps a speaker/provenance label (e.g. "user", "assistant",
// a document name) on every memory ingested in this Remember call, unless
// the extractor already set one. Records who asserted the fact, so
// attribution questions and assistant-statement recall work.
func WithSpeaker(speaker string) RememberOption { return model.WithSpeaker(speaker) }

// WithTrackAccess records the memories this recall returns as accessed
// (access_count, last_accessed_at). Use it where results reach an agent or a
// person, not for benchmarks: access lifts ranking slightly and restarts a
// memory's decay clock.
func WithTrackAccess() RecallOption { return model.WithTrackAccess() }

// WithTraverseAsOf walks the relationships as they held at t.
func WithTraverseAsOf(t time.Time) TraverseOption { return model.WithTraverseAsOf(t) }

// WithTraverseIncludeInvalid walks ended relationships as well.
func WithTraverseIncludeInvalid() TraverseOption { return model.WithTraverseIncludeInvalid() }

// WithoutDecay skips the decay pass.
func WithoutDecay() MaintainOption { return model.WithoutDecay() }

// WithoutExpire skips the expire pass in Maintain.
func WithoutExpire() MaintainOption { return model.WithoutExpire() }

// WithoutProfile skips the profile-refresh pass in Maintain.
func WithoutProfile() MaintainOption { return model.WithoutProfile() }

// WithoutReconcile skips the reconcile pass.
func WithoutReconcile() MaintainOption { return model.WithoutReconcile() }

// WithoutRelate skips the relate pass.
func WithoutRelate() MaintainOption { return model.WithoutRelate() }
