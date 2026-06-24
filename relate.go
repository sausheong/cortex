package cortex

import (
	"context"
	"fmt"
)

// BuildMemoryEdges scans the graph per-entity for non-contradicting relations
// (derives/extends) among currently-valid memories and records them as memory
// edges. Detection is delegated to the configured LLM if it implements
// RelationDetector; if it does not (or none is configured), the report is
// marked Skipped. The record-vs-reject decision is made by a deterministic
// gate here, not by the LLM. Edge writes are idempotent (PutMemoryEdge), so
// re-running does not stack edges. This never modifies or deletes memories.
func (c *Cortex) BuildMemoryEdges(ctx context.Context, opts ...RelateOption) (RelationReport, error) {
	cfg := &relateConfig{}
	for _, o := range opts {
		o(cfg)
	}

	det, ok := c.cfg.llm.(RelationDetector)
	if c.cfg.llm == nil || !ok {
		return RelationReport{
			Skipped:    true,
			SkipReason: "no RelationDetector-capable LLM configured",
		}, nil
	}

	var report RelationReport

	// Dedup proposals across entity passes; keyed by an order-insensitive
	// pair+type signature so an (a,b,extends) seen under one shared entity
	// isn't re-proposed under another.
	seen := map[string]bool{}

	entityIDs, err := c.allEntityIDs(ctx)
	if err != nil {
		return RelationReport{}, err
	}

	for _, eid := range entityIDs {
		mems, err := c.GetMemoriesByEntity(ctx, eid) // currently-valid only
		if err != nil {
			return RelationReport{}, err
		}
		if len(mems) < 2 {
			continue
		}
		report.EntitiesScanned++
		report.MemoriesScanned += len(mems)

		rels, err := det.DetectRelations(ctx, mems)
		if err != nil {
			return RelationReport{}, fmt.Errorf("cortex: detect relations for entity %s: %w", eid, err)
		}

		inSet := make(map[string]Memory, len(mems))
		for _, m := range mems {
			inSet[m.ID] = m
		}

		for _, r := range rels {
			reason, okGate := c.gateRelation(ctx, r, inSet, seen)
			if !okGate {
				report.Rejected = append(report.Rejected, RejectedRelation{
					SourceID: r.SourceID, TargetID: r.TargetID, Type: r.Type, Reason: reason,
				})
				continue
			}
			edge := &MemoryEdge{SourceID: r.SourceID, TargetID: r.TargetID, Type: r.Type}
			if err := c.PutMemoryEdge(ctx, edge); err != nil {
				return report, fmt.Errorf("cortex: record %s edge: %w", r.Type, err)
			}
			src := inSet[r.SourceID]
			tgt := inSet[r.TargetID]
			report.Proposed = append(report.Proposed, RelationProposal{
				SourceID: r.SourceID, SourceContent: src.Content,
				TargetID: r.TargetID, TargetContent: tgt.Content,
				Type: r.Type, Reason: r.Reason,
			})
		}
	}
	return report, nil
}

// gateRelation is the deterministic gate. Returns ("", true) to record, or
// (reason, false) to reject.
func (c *Cortex) gateRelation(ctx context.Context, r MemoryRelation, inSet map[string]Memory, seen map[string]bool) (string, bool) {
	if r.Type != EdgeDerives && r.Type != EdgeExtends {
		return "unsupported relation type", false
	}
	if r.SourceID == r.TargetID {
		return "self-loop", false
	}
	if _, ok := inSet[r.SourceID]; !ok {
		return "source id not in candidate set", false
	}
	if _, ok := inSet[r.TargetID]; !ok {
		return "target id not in candidate set", false
	}
	// Order-insensitive signature for this type, so (a,b) and (b,a) collide.
	a, b := r.SourceID, r.TargetID
	if a > b {
		a, b = b, a
	}
	sig := a + "\x00" + b + "\x00" + r.Type
	if seen[sig] {
		return "duplicate relation in this run", false
	}
	// Already-persisted edge of this type in either direction?
	existing, err := c.GetMemoryEdgesByType(ctx, r.SourceID, r.Type)
	if err == nil {
		for _, e := range existing {
			if (e.SourceID == r.SourceID && e.TargetID == r.TargetID) ||
				(e.SourceID == r.TargetID && e.TargetID == r.SourceID) {
				return "edge already exists", false
			}
		}
	}
	seen[sig] = true
	return "", true
}
