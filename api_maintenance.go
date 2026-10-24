package cortex

import (
	"context"

	"github.com/sausheong/cortex/internal/maintain"
)

// ProposeCompaction proposes merges, combinations and rollups of this user's
// live memories using the user's LLM. Only the active embedding generation is
// read. Nothing is written.
func (c *Cortex) ProposeCompaction(ctx context.Context) ([]Change, error) {
	return maintain.New(c.h).ProposeCompaction(ctx)
}

// ApplyCompaction writes each change's result as a new memory with a derives
// edge to every source, and soft-deletes the sources (by "compaction"), all
// in one batch. Every source must be one of this user's live memories, else
// ErrNotFound is returned and nothing is written.
func (c *Cortex) ApplyCompaction(ctx context.Context, changes []Change) (string, error) {
	return maintain.New(c.h).ApplyCompaction(ctx, changes)
}

// UndoCompaction restores the sources of a compaction batch and soft-deletes
// the memories the compaction wrote. A batch that is not this user's is
// ErrNotFound.
func (c *Cortex) UndoCompaction(ctx context.Context, batchID string) error {
	return maintain.New(c.h).UndoCompaction(ctx, batchID)
}

// ExpireMemories soft-retires currently-valid memories whose ForgetAfter has
// passed. Retirement reuses InvalidateMemory (expired_at = now, invalid_at
// left NULL) — the memory drops out of default recall but stays reachable via
// as_of / include_invalid. Static memories are NOT exempt: an explicit expiry
// is a stronger, more specific signal than the static classification, so TTL
// wins. Dry-run computes the report without writing.
func (c *Cortex) ExpireMemories(ctx context.Context, opts ...ExpireOption) (ExpireReport, error) {
	return maintain.New(c.h).ExpireMemories(ctx, opts...)
}

// DecayConfidence applies age-based exponential decay to the confidence of
// currently-valid memories and auto-soft-invalidates (prunes) any whose
// decayed confidence falls below the floor. Decay is anchored at each
// memory's last_decay_at (or created_at if never decayed) and the anchor is
// advanced to now on each applied pass, so the operation composes: running it
// on any cadence converges to the same confidence for a given memory age.
// A memory recall has surfaced (see WithTrackAccess) decays from its last
// access instead, so memories in use fade slower than ones nobody asks for.
// Pruning is soft (expired_at only, invalid_at left NULL) — the memory is
// system-retired, not marked false; it stays reachable via as_of /
// include_invalid. Dry-run computes the report without writing anything.
func (c *Cortex) DecayConfidence(ctx context.Context, opts ...DecayOption) (DecayReport, error) {
	return maintain.New(c.h).DecayConfidence(ctx, opts...)
}

// Maintain runs the reconsolidation passes — reconcile, relate, decay, expire,
// profile — in a fixed order as one idempotent-in-effect background pass
// (cortex's "dreaming"). It is pure orchestration: it composes
// ApplyReconcile/Reconcile, BuildMemoryEdges, DecayConfidence, ExpireMemories,
// and RefreshProfiles, collecting each sub-report. Each pass is independently
// skippable via options. A Skipped sub-report (e.g. reconcile or relate without
// a detector-capable LLM) is recorded, not an error; only a real engine error
// aborts the pass. Under dry-run, reconcile uses its dry-run path, decay uses
// WithDecayDryRun, and relate, expire, and profile are skipped (relate edges are
// additive with no dry-run mode; expire and profile both write), so a dry-run
// Maintain writes nothing.
//
// Order rationale: reconcile first (retire contradictions before linking),
// relate second (link the reconciled set), decay third (age/prune the settled
// set), expire fourth (retire memories past their forget_after), profile last
// (refresh owner + tracked entities' digests over the settled, pruned set).
func (c *Cortex) Maintain(ctx context.Context, opts ...MaintainOption) (MaintainReport, error) {
	return maintain.New(c.h).Maintain(ctx, opts...)
}

// Reconcile scans the graph per-entity for contradicting memories and returns
// a report of proposed supersessions WITHOUT changing anything (dry-run).
// Detection is delegated to the configured LLM if it implements Reconciler;
// if it does not (or none is configured), the report is marked Skipped.
// The supersede-vs-keep decision is made by a deterministic gate here, not by
// the LLM: a flagged pair is only proposed when the superseding memory is
// strictly newer than the stale one and both are currently valid.
//
// Use ApplyReconcile to actually invalidate the proposed stale memories.
func (c *Cortex) Reconcile(ctx context.Context, opts ...ReconcileOption) (ReconcileReport, error) {
	return maintain.New(c.h).Reconcile(ctx, opts...)
}

// ApplyReconcile RE-RUNS detection (it calls Reconcile, a fresh LLM pass) and
// then soft-invalidates each proposed stale memory (setting invalid_at to the
// superseding memory's created_at). It does NOT consume a prior dry-run report:
// if the graph changed or the LLM returned different output since an earlier
// Reconcile call, the applied set may differ from what that dry-run showed. The
// deterministic gate still bounds what can be applied (strictly-newer + both
// currently valid), so divergence is limited to which gated pairs surface.
// Returns the report describing what was applied. On the first invalidation
// error it returns that error; supersessions applied before the failure remain
// applied (soft-invalidation is reversible). Returns the Skipped report
// unchanged when no Reconciler is configured.
func (c *Cortex) ApplyReconcile(ctx context.Context, opts ...ReconcileOption) (ReconcileReport, error) {
	return maintain.New(c.h).ApplyReconcile(ctx, opts...)
}

// ApplyReconcileReport applies a previously-produced reconcile report WITHOUT
// re-running detection — this is the reviewed-apply path that closes the gap
// where a dry-run and a separate --apply could diverge. Each proposed
// supersession is re-validated against current state before invalidation:
// the stale memory must still be currently valid, the superseding memory must
// still exist, be currently valid, and be strictly newer than the stale one.
// Proposals that no longer hold are skipped and recorded in the returned
// report's Rejected list with a reason. A Skipped input report is returned
// unchanged. The returned report's Proposed list contains only the
// supersessions that were actually applied.
func (c *Cortex) ApplyReconcileReport(ctx context.Context, report ReconcileReport) (ReconcileReport, error) {
	return maintain.New(c.h).ApplyReconcileReport(ctx, report)
}

// BuildMemoryEdges scans the graph per-entity for non-contradicting relations
// (derives/extends) among currently-valid memories and records them as memory
// edges. Detection is delegated to the configured LLM if it implements
// RelationDetector; if it does not (or none is configured), the report is
// marked Skipped. The record-vs-reject decision is made by a deterministic
// gate here, not by the LLM. Edge writes are idempotent (PutMemoryEdge), so
// re-running does not stack edges. This never modifies or deletes memories.
func (c *Cortex) BuildMemoryEdges(ctx context.Context, opts ...RelateOption) (RelationReport, error) {
	return maintain.New(c.h).BuildMemoryEdges(ctx, opts...)
}

// TrackProfile marks an entity for automatic profile refresh during Maintain.
// The owner entity is always eligible regardless of this marker.
func (c *Cortex) TrackProfile(ctx context.Context, entityID string) error {
	return maintain.New(c.h).TrackProfile(ctx, entityID)
}

// UntrackProfile removes the tracking marker and drops any cached digest.
func (c *Cortex) UntrackProfile(ctx context.Context, entityID string) error {
	return maintain.New(c.h).UntrackProfile(ctx, entityID)
}

// Profile returns the entity's context digest. It serves a cached digest when
// fresh, otherwise rebuilds and caches it. Freshness fails (forcing a rebuild)
// when there is no cache, the cache is older than the TTL, or the entity's
// current valid-memory count differs from the cached count.
func (c *Cortex) Profile(ctx context.Context, entityID string, opts ...ProfileOption) (Profile, error) {
	return maintain.New(c.h).Profile(ctx, entityID, opts...)
}

// CachedProfile returns the owner entity's stored digest without ever
// building one or calling a model. ok is false when the user has no owner
// entity or no digest has been built yet.
func (c *Cortex) CachedProfile(ctx context.Context) (Profile, bool, error) {
	return maintain.New(c.h).CachedProfile(ctx)
}

// RefreshProfiles rebuilds the cached digest for every profile-eligible entity
// (owner + tracked). It forces a rebuild regardless of TTL so a scheduled
// Maintain run always refreshes. A single entity's build failure is recorded
// in the report's Skipped list and does not abort the pass.
func (c *Cortex) RefreshProfiles(ctx context.Context, opts ...ProfileOption) (ProfileReport, error) {
	return maintain.New(c.h).RefreshProfiles(ctx, opts...)
}
