package cortex

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const (
	defaultDecayHalfLife = 90 * 24 * time.Hour // 90 days
	defaultDecayFloor    = 0.05
)

// DecayConfidence applies age-based exponential decay to the confidence of
// currently-valid memories and auto-soft-invalidates (prunes) any whose
// decayed confidence falls below the floor. Decay is anchored at each
// memory's last_decay_at (or created_at if never decayed) and the anchor is
// advanced to now on each applied pass, so the operation composes: running it
// on any cadence converges to the same confidence for a given memory age.
// Pruning is soft (expired_at only, invalid_at left NULL) — the memory is
// system-retired, not marked false; it stays reachable via as_of /
// include_invalid. Dry-run computes the report without writing anything.
func (c *Cortex) DecayConfidence(ctx context.Context, opts ...DecayOption) (DecayReport, error) {
	cfg := &decayConfig{halfLife: defaultDecayHalfLife, floor: defaultDecayFloor}
	for _, o := range opts {
		o(cfg)
	}

	now := time.Now().UTC()
	report := DecayReport{DryRun: cfg.dryRun}

	rows, err := c.db.QueryContext(ctx,
		`SELECT id, content, confidence, created_at, last_decay_at
		 FROM memories
		 WHERE `+currentlyValidClause("")+` AND static = 0`,
	)
	if err != nil {
		return DecayReport{}, fmt.Errorf("cortex: decay scan: %w", err)
	}

	type candidate struct {
		id      string
		content string
		conf    float64
		anchor  time.Time
	}
	var cands []candidate
	for rows.Next() {
		var id, content string
		var conf float64
		var createdAt time.Time
		var lastDecay sql.NullTime
		if err := rows.Scan(&id, &content, &conf, &createdAt, &lastDecay); err != nil {
			rows.Close()
			return DecayReport{}, fmt.Errorf("cortex: decay scan row: %w", err)
		}
		anchor := createdAt
		if lastDecay.Valid {
			anchor = lastDecay.Time
		}
		cands = append(cands, candidate{id: id, content: content, conf: conf, anchor: anchor})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return DecayReport{}, fmt.Errorf("cortex: decay iterate: %w", err)
	}
	rows.Close()

	for _, cd := range cands {
		report.Scanned++
		elapsed := now.Sub(cd.anchor)
		newConf := decayedConfidence(cd.conf, elapsed, cfg.halfLife)
		if newConf == cd.conf {
			continue // no measurable change
		}
		pruned := newConf < cfg.floor
		report.Changes = append(report.Changes, DecayChange{
			ID: cd.id, Content: cd.content,
			OldConfidence: cd.conf, NewConfidence: newConf, Pruned: pruned,
		})
		report.Decayed++
		if pruned {
			report.Pruned++
		}

		if cfg.dryRun {
			continue
		}

		if _, err := c.db.ExecContext(ctx,
			`UPDATE memories SET confidence = ?, last_decay_at = ? WHERE id = ?`,
			newConf, now, cd.id,
		); err != nil {
			return report, fmt.Errorf("cortex: decay update %s: %w", cd.id, err)
		}
		if pruned {
			if err := c.InvalidateMemory(ctx, cd.id, nil); err != nil {
				return report, fmt.Errorf("cortex: decay prune %s: %w", cd.id, err)
			}
		}
	}

	return report, nil
}
