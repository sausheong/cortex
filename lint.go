package cortex

import (
	"context"
	"database/sql"
	"fmt"
)

const defaultLowConfidenceThreshold = 0.3

// Lint scans the graph and returns a structured report of cleanup
// candidates. Pure read operation — never mutates the graph.
func (c *Cortex) Lint(ctx context.Context, opts ...LintOption) (LintReport, error) {
	cfg := &lintConfig{lowConfidenceThreshold: defaultLowConfidenceThreshold}
	for _, o := range opts {
		o(cfg)
	}

	var r LintReport
	var err error

	if r.EntityCount, err = countRows(ctx, c.db, "entities"); err != nil {
		return r, fmt.Errorf("cortex: lint: count entities: %w", err)
	}
	if r.RelationshipCount, err = countRows(ctx, c.db, "relationships"); err != nil {
		return r, fmt.Errorf("cortex: lint: count relationships: %w", err)
	}
	if r.MemoryCount, err = countRows(ctx, c.db, "memories"); err != nil {
		return r, fmt.Errorf("cortex: lint: count memories: %w", err)
	}

	if r.Orphans, err = findOrphans(ctx, c.db); err != nil {
		return r, fmt.Errorf("cortex: lint: orphans: %w", err)
	}
	if r.EntitiesNoMemories, err = findEntitiesNoMemories(ctx, c.db); err != nil {
		return r, fmt.Errorf("cortex: lint: entities-no-memories: %w", err)
	}
	if r.NearDuplicates, err = findNearDuplicates(ctx, c.db); err != nil {
		return r, fmt.Errorf("cortex: lint: near-duplicates: %w", err)
	}
	if r.DeadSources, err = findDeadSources(ctx, c.db); err != nil {
		return r, fmt.Errorf("cortex: lint: dead-sources: %w", err)
	}
	if r.UnlinkedMemories, err = findUnlinkedMemories(ctx, c.db); err != nil {
		return r, fmt.Errorf("cortex: lint: unlinked-memories: %w", err)
	}
	if cfg.lowConfidence {
		if r.LowConfidenceMemories, err = findLowConfidenceMemories(ctx, c.db, cfg.lowConfidenceThreshold); err != nil {
			return r, fmt.Errorf("cortex: lint: low-confidence: %w", err)
		}
	}

	return r, nil
}

func countRows(ctx context.Context, db *sql.DB, table string) (int, error) {
	var n int
	// Table name is hard-coded by caller — safe to interpolate.
	err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&n)
	return n, err
}

func findOrphans(ctx context.Context, db *sql.DB) ([]EntityRef, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT e.id, e.name, e.type FROM entities e
		WHERE NOT EXISTS (SELECT 1 FROM relationships WHERE source_id = e.id OR target_id = e.id)
		  AND NOT EXISTS (SELECT 1 FROM memory_entities WHERE entity_id = e.id)
		ORDER BY e.type, e.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EntityRef
	for rows.Next() {
		var e EntityRef
		if err := rows.Scan(&e.ID, &e.Name, &e.Type); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func findEntitiesNoMemories(ctx context.Context, db *sql.DB) ([]EntityRef, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT e.id, e.name, e.type FROM entities e
		WHERE NOT EXISTS (SELECT 1 FROM memory_entities WHERE entity_id = e.id)
		  AND EXISTS (SELECT 1 FROM relationships WHERE source_id = e.id OR target_id = e.id)
		ORDER BY e.type, e.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EntityRef
	for rows.Next() {
		var e EntityRef
		if err := rows.Scan(&e.ID, &e.Name, &e.Type); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func findNearDuplicates(ctx context.Context, db *sql.DB) ([]DuplicatePair, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT a.id, a.name, a.type, b.id, b.name, b.type
		FROM entities a
		JOIN entities b
		  ON a.type = b.type
		 AND LOWER(a.name) = LOWER(b.name)
		 AND a.id < b.id
		ORDER BY a.type, LOWER(a.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DuplicatePair
	for rows.Next() {
		var p DuplicatePair
		if err := rows.Scan(&p.A.ID, &p.A.Name, &p.A.Type, &p.B.ID, &p.B.Name, &p.B.Type); err != nil {
			return nil, err
		}
		p.Type = p.A.Type
		out = append(out, p)
	}
	return out, rows.Err()
}

func findDeadSources(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT source FROM memories
		WHERE source IS NOT NULL AND source != ''
		  AND source NOT IN (
			SELECT DISTINCT source FROM entities
			WHERE source IS NOT NULL AND source != ''
		  )
		ORDER BY source`)
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

func findUnlinkedMemories(ctx context.Context, db *sql.DB) ([]MemoryRef, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, content, source, confidence FROM memories m
		WHERE NOT EXISTS (SELECT 1 FROM memory_entities WHERE memory_id = m.id)
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMemoryRefs(rows)
}

func findLowConfidenceMemories(ctx context.Context, db *sql.DB, threshold float64) ([]MemoryRef, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, content, source, confidence FROM memories
		WHERE confidence < ?
		ORDER BY confidence ASC`, threshold)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMemoryRefs(rows)
}

func scanMemoryRefs(rows *sql.Rows) ([]MemoryRef, error) {
	var out []MemoryRef
	for rows.Next() {
		var m MemoryRef
		if err := rows.Scan(&m.ID, &m.Content, &m.Source, &m.Confidence); err != nil {
			return nil, err
		}
		m.Content = truncate(m.Content, 80)
		out = append(out, m)
	}
	return out, rows.Err()
}

// truncate returns s if shorter than n, else s[:n] + "...".
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
