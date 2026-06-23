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

	// Apply path is Task 5; dry-run returns here.
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
