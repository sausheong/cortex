package maintain

import (
	"context"
	"fmt"
	"time"

	"github.com/sausheong/cortex/internal/memory"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
)

// ExpireMemories soft-retires currently-valid memories whose ForgetAfter has
// passed. Retirement reuses InvalidateMemory (expired_at = now, invalid_at
// left NULL) — the memory drops out of default recall but stays reachable via
// as_of / include_invalid. Static memories are NOT exempt: an explicit expiry
// is a stronger, more specific signal than the static classification, so TTL
// wins. Dry-run computes the report without writing.
func (c Ops) ExpireMemories(ctx context.Context, opts ...model.ExpireOption) (model.ExpireReport, error) {
	cfg := &model.ExpireConfig{}
	for _, o := range opts {
		o(cfg)
	}

	now := time.Now().UTC()
	report := model.ExpireReport{DryRun: cfg.DryRun}

	rows, err := c.DB.QueryContext(ctx,
		`SELECT id, content_ct, forget_after
		 FROM memories
		 WHERE user_id = ? AND deleted_at IS NULL AND `+model.CurrentlyValidClause("")+` AND forget_after IS NOT NULL AND forget_after <= ?`,
		c.UserID, now,
	)
	if err != nil {
		return model.ExpireReport{}, fmt.Errorf("cortex: expire scan: %w", err)
	}

	type candidate struct {
		id      string
		content string
		fa      time.Time
	}
	var cands []candidate
	for rows.Next() {
		var id string
		var ct []byte
		var fa time.Time
		if err := rows.Scan(&id, &ct, &fa); err != nil {
			rows.Close()
			return model.ExpireReport{}, fmt.Errorf("cortex: expire scan row: %w", err)
		}
		content, err := c.OpenStr(store.TblMemories, id, "content", ct)
		if err != nil {
			rows.Close()
			return model.ExpireReport{}, err
		}
		cands = append(cands, candidate{id: id, content: content, fa: fa})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return model.ExpireReport{}, fmt.Errorf("cortex: expire iterate: %w", err)
	}
	rows.Close()

	for _, cd := range cands {
		report.Scanned++
		report.Changes = append(report.Changes, model.ExpireChange{
			ID: cd.id, Content: cd.content, ForgetAfter: cd.fa,
		})
		report.Expired++
		if cfg.DryRun {
			continue
		}
		if err := memory.New(c.Handle).InvalidateMemory(ctx, cd.id, nil); err != nil {
			return report, fmt.Errorf("cortex: expire retire %s: %w", cd.id, err)
		}
	}

	return report, nil
}
