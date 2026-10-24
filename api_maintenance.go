package cortex

import (
	"context"
	"time"

	"github.com/sausheong/cortex/internal/graph"
	"github.com/sausheong/cortex/internal/maintain"
	"github.com/sausheong/cortex/internal/render"
	"github.com/sausheong/cortex/internal/tenancy"
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

// CountMemories returns how many live memories SoftDeleteMemories would mark
// for f, so a caller can show the exact number before deleting.
func (c *Cortex) CountMemories(ctx context.Context, f DeleteFilter) (int, error) {
	return graph.New(c.h).CountMemories(ctx, f)
}

// SampleMemories returns up to limit of the live memories SoftDeleteMemories
// would mark for f, newest first, so a caller can show what is about to go.
func (c *Cortex) SampleMemories(ctx context.Context, f DeleteFilter, limit int) ([]Memory, error) {
	return graph.New(c.h).SampleMemories(ctx, f, limit)
}

// CountByConnection returns how many rows SoftDeleteByConnection would mark
// for connID (memories, entities, chunks and relationships, including
// relationships that touch the connection's entities).
func (c *Cortex) CountByConnection(ctx context.Context, connID string) (int, error) {
	return graph.New(c.h).CountByConnection(ctx, connID)
}

// SoftDeleteMemories marks the memories matching f as deleted in one batch.
// The batch ID is empty when nothing matched.
func (c *Cortex) SoftDeleteMemories(ctx context.Context, f DeleteFilter, by string) (string, int, error) {
	return graph.New(c.h).SoftDeleteMemories(ctx, f, by)
}

// SoftDeleteEntity soft-deletes an entity, its relationships and its chunks.
func (c *Cortex) SoftDeleteEntity(ctx context.Context, id string, by string) (string, error) {
	return graph.New(c.h).SoftDeleteEntity(ctx, id, by)
}

// SoftDeleteByConnection soft-deletes everything the connection wrote:
// memories, entities, relationships and chunks, plus relationships that touch
// the removed entities. n counts all rows marked.
func (c *Cortex) SoftDeleteByConnection(ctx context.Context, connID string) (string, int, error) {
	return graph.New(c.h).SoftDeleteByConnection(ctx, connID)
}

// RestoreBatch reverses a batch of this user: undoes recorded merge changes
// (newest first), then un-deletes the batch's rows. An entity whose (name,
// type) is now taken by a live entity is merged into it instead.
func (c *Cortex) RestoreBatch(ctx context.Context, batchID string) error {
	return graph.New(c.h).RestoreBatch(ctx, batchID)
}

// ListDeleted returns this user's soft-deleted memories, newest deletion first.
func (c *Cortex) ListDeleted(ctx context.Context, limit int, offset int) ([]Memory, error) {
	return graph.New(c.h).ListDeleted(ctx, limit, offset)
}

// ListDeleteBatches lists this user's unrestored batches, newest first.
func (c *Cortex) ListDeleteBatches(ctx context.Context) ([]DeleteBatch, error) {
	return graph.New(c.h).ListDeleteBatches(ctx)
}

// PurgeDeleted hard-deletes this user's rows soft-deleted more than olderThan
// ago, with their embeddings (every generation), memory links and memory
// edges, in one transaction. Batches older than the cutoff, or already
// restored, are removed with their change records. It returns the number of
// memories, entities, relationships and chunks removed.
func (c *Cortex) PurgeDeleted(ctx context.Context, olderThan time.Duration) (int, error) {
	return graph.New(c.h).PurgeDeleted(ctx, olderThan)
}

// SoftDeleteChunks marks the chunks with the given IDs as deleted in one
// batch. The batch ID is empty when nothing matched.
func (c *Cortex) SoftDeleteChunks(ctx context.Context, ids []string, by string) (string, int, error) {
	return graph.New(c.h).SoftDeleteChunks(ctx, ids, by)
}

// GetSyncState returns the last-known sync state for a connector.
func (c *Cortex) GetSyncState(ctx context.Context, connector string) (string, error) {
	return c.h.GetSyncState(ctx, connector)
}

// SetSyncState upserts the sync state for a connector.
func (c *Cortex) SetSyncState(ctx context.Context, connector, state string) error {
	return c.h.SetSyncState(ctx, connector, state)
}

// CreateUser inserts a user and its person entity (Source "owner"), whose ID
// is stored in users.owner_entity_id.
func (c *Cortex) CreateUser(ctx context.Context, googleSub, email, name string, emailVerified bool) (*User, error) {
	return tenancy.New(c.h).CreateUser(ctx, googleSub, email, name, emailVerified)
}

// UserByGoogleSub returns the user with the given Google subject, or ErrNotFound.
func (c *Cortex) UserByGoogleSub(ctx context.Context, sub string) (*User, error) {
	return tenancy.New(c.h).UserByGoogleSub(ctx, sub)
}

// ListUsers returns every user, oldest first.
func (c *Cortex) ListUsers(ctx context.Context) ([]User, error) {
	return tenancy.New(c.h).ListUsers(ctx)
}

// BindBootstrap attaches a Google identity to the bootstrap user, keeping
// its existing data. It fails if the bootstrap user is already bound.
func (c *Cortex) BindBootstrap(ctx context.Context, googleSub, email, name string) (*User, error) {
	return tenancy.New(c.h).BindBootstrap(ctx, googleSub, email, name)
}

// ResolveUserRef resolves an admin reference: a user ID, else a verified
// email. Several matches is an error listing the IDs; none is ErrNotFound.
func (c *Cortex) ResolveUserRef(ctx context.Context, ref string) (*User, error) {
	return tenancy.New(c.h).ResolveUserRef(ctx, ref)
}

// OwnerEntityID returns the ID of the handle's user's owner (person) entity,
// or ErrNotFound when the user has none.
func (c *Cortex) OwnerEntityID(ctx context.Context) (string, error) {
	return tenancy.New(c.h).OwnerEntityID(ctx)
}

// ComputeUsage measures this user's rows now. Bytes is the sum of
// pg_column_size over every row the user owns, including embeddings of all
// generations and soft-deleted rows (they still occupy space until purged).
func (c *Cortex) ComputeUsage(ctx context.Context) (Usage, error) {
	return tenancy.New(c.h).ComputeUsage(ctx)
}

// RefreshUsage computes the usage and stores it in users.usage_json (the
// nightly job calls this).
func (c *Cortex) RefreshUsage(ctx context.Context) (Usage, error) {
	return tenancy.New(c.h).RefreshUsage(ctx)
}

// CachedUsage returns the figures stored by the nightly job. ok is false
// when none are cached yet; it never measures, so it is cheap on a page load.
func (c *Cortex) CachedUsage(ctx context.Context) (u Usage, ok bool, err error) {
	return tenancy.New(c.h).CachedUsage(ctx)
}

// Usage returns the cached figures, or computes them live when none are cached.
func (c *Cortex) Usage(ctx context.Context) (Usage, error) {
	return tenancy.New(c.h).Usage(ctx)
}

// NewMemoriesSince groups this user's live memories created after since by
// the connection that wrote them, with each group's newest perGroup
// memories. Groups are ordered by their newest memory, newest first. It is
// one indexed query and never calls a model.
func (c *Cortex) NewMemoriesSince(ctx context.Context, since time.Time, perGroup int) ([]ConnectionNew, error) {
	return tenancy.New(c.h).NewMemoriesSince(ctx, since, perGroup)
}

// WrittenSince counts this user's live memories created after since, per
// writing connection ID ("" for none).
func (c *Cortex) WrittenSince(ctx context.Context, since time.Time) (map[string]int, error) {
	return tenancy.New(c.h).WrittenSince(ctx, since)
}

// ListMemories pages this user's live memories, newest first unless
// f.Oldest, and returns the total number matching the filter.
func (c *Cortex) ListMemories(ctx context.Context, f MemoryListFilter, limit, offset int) ([]Memory, int, error) {
	return tenancy.New(c.h).ListMemories(ctx, f, limit, offset)
}

// AppliedMigrations returns the versions recorded in schema_migrations, ascending.
func (c *Cortex) AppliedMigrations(ctx context.Context) ([]int, error) {
	return c.h.AppliedMigrations(ctx)
}

// RenderDecayMarkdown renders a DecayReport as human-readable markdown.
func RenderDecayMarkdown(r DecayReport) string { return render.RenderDecayMarkdown(r) }

// RenderExpireMarkdown renders a ExpireReport as human-readable markdown,
// mirroring RenderDecayMarkdown.
func RenderExpireMarkdown(r ExpireReport) string { return render.RenderExpireMarkdown(r) }

// RenderLintMarkdown formats a LintReport as a human-readable markdown
// document. Exported wrapper for CLI consumption.
func RenderLintMarkdown(r LintReport) string { return render.RenderLintMarkdown(r) }

// RenderMaintainMarkdown renders a MaintainReport by composing the per-pass
// renders under a single header. Sub-passes toggled off (nil) are noted as
// skipped.
func RenderMaintainMarkdown(r MaintainReport) string { return render.RenderMaintainMarkdown(r) }

// RenderProfileMarkdown renders a Profile as a human-readable markdown digest.
func RenderProfileMarkdown(p Profile) string { return render.RenderProfileMarkdown(p) }

// RenderProfileReportMarkdown renders a ProfileReport (the Maintain pass
// summary).
func RenderProfileReportMarkdown(r ProfileReport) string {
	return render.RenderProfileReportMarkdown(r)
}

// RenderReconcileMarkdown renders a ReconcileReport as a human-readable
// markdown summary, mirroring RenderLintMarkdown. It uses dry-run wording
// ("Proposed", "Rejected by gate"); use RenderReconcileMarkdownMode to render
// the applied-path wording.
func RenderReconcileMarkdown(r ReconcileReport) string { return render.RenderReconcileMarkdown(r) }

// RenderReconcileMarkdownMode renders a ReconcileReport as a human-readable
// markdown summary. When applied is false the report describes a dry-run
// ("Proposed Supersessions", "Rejected by gate"); when applied is true the
// report describes the result of an apply run ("Applied Supersessions",
// "Skipped (re-validation)"), where r.Proposed lists what was applied and
// r.Rejected lists re-validation skips.
func RenderReconcileMarkdownMode(r ReconcileReport, applied bool) string {
	return render.RenderReconcileMarkdownMode(r, applied)
}

// RenderRelationMarkdown renders a RelationReport as human-readable markdown,
// mirroring RenderReconcileMarkdown.
func RenderRelationMarkdown(r RelationReport) string { return render.RenderRelationMarkdown(r) }
