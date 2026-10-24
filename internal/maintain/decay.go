package maintain

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/sausheong/cortex/internal/memory"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/vecmath"
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
// A memory recall has surfaced (see WithTrackAccess) decays from its last
// access instead, so memories in use fade slower than ones nobody asks for.
// Pruning is soft (expired_at only, invalid_at left NULL) — the memory is
// system-retired, not marked false; it stays reachable via as_of /
// include_invalid. Dry-run computes the report without writing anything.
func (c Ops) DecayConfidence(ctx context.Context, opts ...model.DecayOption) (model.DecayReport, error) {
	cfg := &model.DecayConfig{HalfLife: defaultDecayHalfLife, Floor: defaultDecayFloor}
	for _, o := range opts {
		o(cfg)
	}

	now := time.Now().UTC()
	report := model.DecayReport{DryRun: cfg.DryRun}

	rows, err := c.DB.QueryContext(ctx,
		`SELECT id, content, confidence, created_at, last_decay_at, last_accessed_at
		 FROM memories
		 WHERE user_id = ? AND deleted_at IS NULL AND `+model.CurrentlyValidClause("")+` AND NOT static`,
		c.UserID,
	)
	if err != nil {
		return model.DecayReport{}, fmt.Errorf("cortex: decay scan: %w", err)
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
		var lastDecay, lastAccess sql.NullTime
		if err := rows.Scan(&id, &content, &conf, &createdAt, &lastDecay, &lastAccess); err != nil {
			rows.Close()
			return model.DecayReport{}, fmt.Errorf("cortex: decay scan row: %w", err)
		}
		anchor := createdAt
		if lastDecay.Valid {
			anchor = lastDecay.Time
		}
		// Use restarts the clock: a memory recall surfaced since the last
		// pass decays only from that access.
		if lastAccess.Valid && lastAccess.Time.After(anchor) {
			anchor = lastAccess.Time
		}
		cands = append(cands, candidate{id: id, content: content, conf: conf, anchor: anchor})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return model.DecayReport{}, fmt.Errorf("cortex: decay iterate: %w", err)
	}
	rows.Close()

	for _, cd := range cands {
		report.Scanned++
		elapsed := now.Sub(cd.anchor)
		newConf := vecmath.DecayConf(cd.conf, elapsed, cfg.HalfLife)
		if newConf == cd.conf {
			continue // no measurable change
		}
		pruned := newConf < cfg.Floor
		report.Changes = append(report.Changes, model.DecayChange{
			ID: cd.id, Content: cd.content,
			OldConfidence: cd.conf, NewConfidence: newConf, Pruned: pruned,
		})
		report.Decayed++
		if pruned {
			report.Pruned++
		}

		if cfg.DryRun {
			continue
		}

		if _, err := c.DB.ExecContext(ctx,
			`UPDATE memories SET confidence = ?, last_decay_at = ? WHERE id = ? AND user_id = ?`,
			newConf, now, cd.id, c.UserID,
		); err != nil {
			return report, fmt.Errorf("cortex: decay update %s: %w", cd.id, err)
		}
		if pruned {
			if err := memory.New(c.Handle).InvalidateMemory(ctx, cd.id, nil); err != nil {
				return report, fmt.Errorf("cortex: decay prune %s: %w", cd.id, err)
			}
		}
	}

	return report, nil
}
