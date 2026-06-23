package cortex

import (
	"context"
	"fmt"
)

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
	cfg := &reconcileConfig{}
	for _, o := range opts {
		o(cfg)
	}

	rec, ok := c.cfg.llm.(Reconciler)
	if c.cfg.llm == nil || !ok {
		return ReconcileReport{
			Skipped:    true,
			SkipReason: "no Reconciler-capable LLM configured",
		}, nil
	}

	var report ReconcileReport

	// seen dedups proposals across entity passes within a single run: a memory
	// linked to multiple entities is evaluated once per entity, so the same
	// supersession can surface in more than one pass. Keyed StaleID\x00SupersededByID.
	seen := map[string]bool{}

	entityIDs, err := c.allEntityIDs(ctx)
	if err != nil {
		return ReconcileReport{}, err
	}

	for _, eid := range entityIDs {
		mems, err := c.GetMemoriesByEntity(ctx, eid) // currently-valid only (Tier 2a default)
		if err != nil {
			return ReconcileReport{}, err
		}
		if len(mems) < 2 {
			continue
		}
		report.EntitiesScanned++
		report.MemoriesScanned += len(mems)

		pairs, err := rec.DetectConflicts(ctx, mems)
		if err != nil {
			return ReconcileReport{}, fmt.Errorf("cortex: detect conflicts for entity %s: %w", eid, err)
		}

		byID := make(map[string]Memory, len(mems))
		for _, m := range mems {
			byID[m.ID] = m
		}

		for _, p := range pairs {
			stale, sOK := byID[p.StaleID]
			newer, nOK := byID[p.SupersededByID]
			// Deterministic gate.
			if !sOK || !nOK {
				report.Rejected = append(report.Rejected, RejectedPair{
					StaleID: p.StaleID, SupersededByID: p.SupersededByID,
					Reason: "id not in current candidate set",
				})
				continue
			}
			if !newer.CreatedAt.After(stale.CreatedAt) {
				report.Rejected = append(report.Rejected, RejectedPair{
					StaleID: p.StaleID, SupersededByID: p.SupersededByID,
					Reason: "superseding memory is not strictly newer than stale memory",
				})
				continue
			}
			key := stale.ID + "\x00" + newer.ID
			if seen[key] {
				continue // already proposed in an earlier entity pass
			}
			seen[key] = true
			report.Proposed = append(report.Proposed, Supersession{
				StaleID:             stale.ID,
				StaleContent:        stale.Content,
				SupersededByID:      newer.ID,
				SupersededByContent: newer.Content,
				Reason:              p.Reason,
				InvalidAt:           newer.CreatedAt,
			})
		}
	}

	// Dry-run returns here; see ApplyReconcile for the apply path.
	return report, nil
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
	report, err := c.Reconcile(ctx, opts...)
	if err != nil || report.Skipped {
		return report, err
	}
	for _, p := range report.Proposed {
		invalidAt := p.InvalidAt
		if err := c.InvalidateMemory(ctx, p.StaleID, &invalidAt); err != nil {
			return report, fmt.Errorf("cortex: apply supersession (stale %s): %w", p.StaleID, err)
		}
	}
	return report, nil
}

// allEntityIDs returns every entity id in the graph.
func (c *Cortex) allEntityIDs(ctx context.Context) ([]string, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT id FROM entities`)
	if err != nil {
		return nil, fmt.Errorf("cortex: list entity ids: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("cortex: scan entity id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cortex: iterate entity ids: %w", err)
	}
	return ids, nil
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
	if report.Skipped {
		return report, nil
	}

	out := ReconcileReport{
		EntitiesScanned: report.EntitiesScanned,
		MemoriesScanned: report.MemoriesScanned,
	}

	for _, p := range report.Proposed {
		stale, err := c.getMemoryByID(ctx, p.StaleID)
		if err != nil {
			return out, err
		}
		newer, err := c.getMemoryByID(ctx, p.SupersededByID)
		if err != nil {
			return out, err
		}

		reason, ok := revalidateSupersession(stale, newer)
		if !ok {
			out.Rejected = append(out.Rejected, RejectedPair{
				StaleID: p.StaleID, SupersededByID: p.SupersededByID, Reason: reason,
			})
			continue
		}

		invalidAt := p.InvalidAt
		if err := c.InvalidateMemory(ctx, p.StaleID, &invalidAt); err != nil {
			return out, fmt.Errorf("cortex: apply reviewed supersession (stale %s): %w", p.StaleID, err)
		}
		out.Proposed = append(out.Proposed, p)
	}

	return out, nil
}

// revalidateSupersession re-checks the deterministic gate against current
// memory state. Returns ("", true) when the supersession may be applied,
// or (reason, false) when it must be skipped.
func revalidateSupersession(stale, newer *Memory) (string, bool) {
	if stale == nil {
		return "stale memory no longer exists", false
	}
	if newer == nil {
		return "superseding memory no longer exists", false
	}
	if stale.ExpiredAt != nil || stale.InvalidAt != nil {
		return "stale memory no longer current", false
	}
	if newer.ExpiredAt != nil || newer.InvalidAt != nil {
		return "superseding memory no longer current", false
	}
	if !newer.CreatedAt.After(stale.CreatedAt) {
		return "superseding memory no longer strictly newer", false
	}
	return "", true
}
