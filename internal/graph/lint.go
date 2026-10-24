package graph

import (
	"context"
	"fmt"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
)

const defaultLowConfidenceThreshold = 0.3

// Lint scans the graph and returns a structured report of cleanup
// candidates. Pure read operation — never mutates the graph.
func (c Ops) Lint(ctx context.Context, opts ...model.LintOption) (model.LintReport, error) {
	cfg := &model.LintConfig{LowConfidenceThreshold: defaultLowConfidenceThreshold}
	for _, o := range opts {
		o(cfg)
	}

	var r model.LintReport
	var err error

	if r.EntityCount, err = countRows(ctx, c.DB, c.UserID, "entities"); err != nil {
		return r, fmt.Errorf("cortex: lint: count entities: %w", err)
	}
	if r.RelationshipCount, err = countRows(ctx, c.DB, c.UserID, "relationships"); err != nil {
		return r, fmt.Errorf("cortex: lint: count relationships: %w", err)
	}
	if r.MemoryCount, err = countRows(ctx, c.DB, c.UserID, "memories"); err != nil {
		return r, fmt.Errorf("cortex: lint: count memories: %w", err)
	}

	if err := c.lintEnc(ctx, cfg, &r); err != nil {
		return r, err
	}
	return r, nil
}

func countRows(ctx context.Context, db store.DB, userID, table string) (int, error) {
	var n int
	// Table name is hard-coded by caller — safe to interpolate.
	err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE user_id = ? AND deleted_at IS NULL", table), userID).Scan(&n)
	return n, err
}

// nearDuplicateMin is the NameSimilarity at or above which lint reports two
// same-type entities as possible duplicates; nearDuplicateGroupCap bounds the
// pairwise comparison within one type.
const (
	nearDuplicateMin      = 0.75
	nearDuplicateGroupCap = 3000
)
