package cortex

import "context"

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
	cfg := &maintainConfig{}
	for _, o := range opts {
		o(cfg)
	}

	report := MaintainReport{DryRun: cfg.dryRun}

	// 1. Reconcile.
	if !cfg.skipReconcile {
		var rr ReconcileReport
		var err error
		if cfg.dryRun {
			rr, err = c.Reconcile(ctx)
		} else {
			rr, err = c.ApplyReconcile(ctx)
		}
		if err != nil {
			return report, err
		}
		report.Reconcile = &rr
	}

	// 2. Relate. No dry-run mode → skipped under dry-run to keep the
	// write-nothing guarantee.
	if !cfg.skipRelate {
		if cfg.dryRun {
			report.Relate = &RelationReport{
				Skipped:    true,
				SkipReason: "relate has no dry-run; skipped under --dry-run",
			}
		} else {
			rel, err := c.BuildMemoryEdges(ctx)
			if err != nil {
				return report, err
			}
			report.Relate = &rel
		}
	}

	// 3. Decay (+ auto-prune).
	if !cfg.skipDecay {
		decayOpts := cfg.decayOpts
		if cfg.dryRun {
			decayOpts = append(decayOpts, WithDecayDryRun())
		}
		dr, err := c.DecayConfidence(ctx, decayOpts...)
		if err != nil {
			return report, err
		}
		report.Decay = &dr
	}

	// 4. Expire (retire memories past their forget_after). Skipped under
	// dry-run because it writes via InvalidateMemory.
	if !cfg.skipExpire && !cfg.dryRun {
		er, err := c.ExpireMemories(ctx)
		if err != nil {
			return report, err
		}
		report.Expire = &er
	}

	// 5. Profile (refresh owner + tracked entities' digests). Skipped under
	// dry-run because building writes to entity attributes.
	if !cfg.skipProfile && !cfg.dryRun {
		pr, err := c.RefreshProfiles(ctx)
		if err != nil {
			return report, err
		}
		report.Profile = &pr
	}

	return report, nil
}
