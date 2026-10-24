package graph

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"github.com/sausheong/cortex/internal/llmutil"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/ontology"
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

	if r.Orphans, err = findOrphans(ctx, c.DB, c.UserID); err != nil {
		return r, fmt.Errorf("cortex: lint: orphans: %w", err)
	}
	if r.EntitiesNoMemories, err = findEntitiesNoMemories(ctx, c.DB, c.UserID); err != nil {
		return r, fmt.Errorf("cortex: lint: entities-no-memories: %w", err)
	}
	if r.NearDuplicates, err = findNearDuplicates(ctx, c.DB, c.UserID); err != nil {
		return r, fmt.Errorf("cortex: lint: near-duplicates: %w", err)
	}
	if r.DeadSources, err = findDeadSources(ctx, c.DB, c.UserID); err != nil {
		return r, fmt.Errorf("cortex: lint: dead-sources: %w", err)
	}
	if r.UnlinkedMemories, err = findUnlinkedMemories(ctx, c.DB, c.UserID); err != nil {
		return r, fmt.Errorf("cortex: lint: unlinked-memories: %w", err)
	}
	if cfg.LowConfidence {
		if r.LowConfidenceMemories, err = findLowConfidenceMemories(ctx, c.DB, c.UserID, cfg.LowConfidenceThreshold); err != nil {
			return r, fmt.Errorf("cortex: lint: low-confidence: %w", err)
		}
	}

	return r, nil
}

func countRows(ctx context.Context, db store.DB, userID, table string) (int, error) {
	var n int
	// Table name is hard-coded by caller — safe to interpolate.
	err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE user_id = ? AND deleted_at IS NULL", table), userID).Scan(&n)
	return n, err
}

func findOrphans(ctx context.Context, db store.DB, userID string) ([]model.EntityRef, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT e.id, e.name, e.type FROM entities e
		WHERE e.user_id = ? AND e.deleted_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM relationships WHERE user_id = e.user_id AND deleted_at IS NULL AND (source_id = e.id OR target_id = e.id))
		  AND NOT EXISTS (SELECT 1 FROM memory_entities WHERE user_id = e.user_id AND entity_id = e.id)
		ORDER BY e.type, e.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.EntityRef
	for rows.Next() {
		var e model.EntityRef
		if err := rows.Scan(&e.ID, &e.Name, &e.Type); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func findEntitiesNoMemories(ctx context.Context, db store.DB, userID string) ([]model.EntityRef, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT e.id, e.name, e.type FROM entities e
		WHERE e.user_id = ? AND e.deleted_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM memory_entities WHERE user_id = e.user_id AND entity_id = e.id)
		  AND EXISTS (SELECT 1 FROM relationships WHERE user_id = e.user_id AND deleted_at IS NULL AND (source_id = e.id OR target_id = e.id))
		ORDER BY e.type, e.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.EntityRef
	for rows.Next() {
		var e model.EntityRef
		if err := rows.Scan(&e.ID, &e.Name, &e.Type); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// nearDuplicateMin is the NameSimilarity at or above which lint reports two
// same-type entities as possible duplicates; nearDuplicateGroupCap bounds the
// pairwise comparison within one type.
const (
	nearDuplicateMin      = 0.75
	nearDuplicateGroupCap = 3000
)

func findNearDuplicates(ctx context.Context, db store.DB, userID string) ([]model.DuplicatePair, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, name, type FROM entities
		WHERE user_id = ? AND deleted_at IS NULL
		ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := map[string][]model.EntityRef{}
	var types []string
	for rows.Next() {
		var e model.EntityRef
		if err := rows.Scan(&e.ID, &e.Name, &e.Type); err != nil {
			return nil, err
		}
		t := ontology.NormalizeType(e.Type)
		if _, ok := groups[t]; !ok {
			types = append(types, t)
		}
		groups[t] = append(groups[t], e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(types)
	var out []model.DuplicatePair
	for _, t := range types {
		g := groups[t]
		if len(g) > nearDuplicateGroupCap {
			g = g[:nearDuplicateGroupCap]
		}
		var pairs []model.DuplicatePair
		for i := 0; i < len(g); i++ {
			for j := i + 1; j < len(g); j++ {
				if s := NameSimilarity(g[i].Name, g[j].Name); s >= nearDuplicateMin {
					pairs = append(pairs, model.DuplicatePair{Type: t, A: g[i], B: g[j], Score: s})
				}
			}
		}
		sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].Score > pairs[j].Score })
		out = append(out, pairs...)
	}
	return out, nil
}

func findDeadSources(ctx context.Context, db store.DB, userID string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT source FROM memories
		WHERE user_id = ? AND deleted_at IS NULL AND source IS NOT NULL AND source != ''
		  AND source NOT IN (
			SELECT DISTINCT source FROM entities
			WHERE user_id = ? AND deleted_at IS NULL AND source IS NOT NULL AND source != ''
		  )
		ORDER BY source`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func findUnlinkedMemories(ctx context.Context, db store.DB, userID string) ([]model.MemoryRef, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, content, source, confidence FROM memories m
		WHERE m.user_id = ? AND m.deleted_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM memory_entities WHERE user_id = m.user_id AND memory_id = m.id)
		ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMemoryRefs(rows)
}

func findLowConfidenceMemories(ctx context.Context, db store.DB, userID string, threshold float64) ([]model.MemoryRef, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, content, source, confidence FROM memories
		WHERE user_id = ? AND deleted_at IS NULL AND confidence < ?
		ORDER BY confidence ASC`, userID, threshold)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMemoryRefs(rows)
}

func scanMemoryRefs(rows *sql.Rows) ([]model.MemoryRef, error) {
	var out []model.MemoryRef
	for rows.Next() {
		var m model.MemoryRef
		if err := rows.Scan(&m.ID, &m.Content, &m.Source, &m.Confidence); err != nil {
			return nil, err
		}
		m.Content = llmutil.Truncate(m.Content, 80)
		out = append(out, m)
	}
	return out, rows.Err()
}
