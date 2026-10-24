package cortex

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/sausheong/cortex/internal/model"
)

// nullTimePtr converts a sql.NullTime to a *time.Time (nil when not valid).
func nullTimePtr(nt sql.NullTime) *time.Time {
	if !nt.Valid {
		return nil
	}
	t := nt.Time
	return &t
}

// PutMemory inserts a memory and its entity links into the database.
// The operation is wrapped in a transaction. The memory's ID, CreatedAt,
// and UpdatedAt are set on the passed struct.
func (c *Cortex) PutMemory(ctx context.Context, m *Memory) error {
	m.Confidence = model.CoerceConfidence(m.Confidence)
	m.ID = newID()
	now := time.Now().UTC()
	m.CreatedAt = now
	m.UpdatedAt = now

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cortex: begin transaction: %w", err)
	}
	defer tx.Rollback()

	for _, entityID := range m.EntityIDs {
		if err := requireEntity(ctx, tx, c.userID, entityID); err != nil {
			return err
		}
	}

	var speaker any
	if m.Speaker != "" {
		speaker = m.Speaker
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO memories (id, user_id, content, source, speaker, confidence, created_at, updated_at, valid_at, invalid_at, static, forget_after, written_by_connection_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, c.userID, m.Content, m.Source, speaker, m.Confidence, m.CreatedAt, m.UpdatedAt, m.ValidAt, m.InvalidAt, m.Static, m.ForgetAfter, c.nullConn(),
	)
	if err != nil {
		return fmt.Errorf("cortex: insert memory: %w", err)
	}

	// Insert entity links into junction table.
	for _, entityID := range m.EntityIDs {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO memory_entities (user_id, memory_id, entity_id) VALUES (?, ?, ?)`,
			c.userID, m.ID, entityID,
		)
		if err != nil {
			return fmt.Errorf("cortex: insert memory entity link: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cortex: commit memory: %w", err)
	}
	return nil
}

// SearchMemories performs an FTS5 full-text search on memory content,
// ranked by relevance (best first). The query is sanitized into a safe
// FTS5 MATCH expression. Entity links are loaded from memory_entities for
// each result.
func (c *Cortex) SearchMemories(ctx context.Context, query string, limit int) ([]Memory, error) {
	return c.searchMemoriesMode(ctx, query, limit, model.TemporalMode{})
}

// searchMemoriesMode is the temporal-mode-aware backing for SearchMemories.
// The validity predicate is derived from mode (default = currently-valid).
func (c *Cortex) searchMemoriesMode(ctx context.Context, query string, limit int, mode model.TemporalMode) ([]Memory, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}

	clause, targs := mode.Clause("m")
	// Arg order: temporal args, limit, then the tsquery tokens. The '?'
	// placeholders are numbered 1..n by rebind; the tsquery expression uses
	// explicit $n numbers that start after them.
	// Arg order: user id (df), user id (corpus size), user id (rows), temporal
	// args, limit, then the tsquery tokens at explicit $n numbers.
	tokExprs, tsArgs := tsTokenExprs(query, len(targs)+5)
	if len(tokExprs) == 0 {
		return nil, nil
	}
	args := []any{c.userID, c.userID, c.userID}
	args = append(args, targs...)
	args = append(args, limit)
	args = append(args, tsArgs...)

	// Ranking is IDF-weighted: each matching query token contributes
	// ln(1 + N/df), so rare words outweigh common ones (ts_rank_cd has no IDF
	// and let question filler words outrank the distinctive keyword), divided
	// by sqrt(distinct lexemes) as a mild length normalisation. Recalibrated
	// against the SQLite BM25 baseline with cortex-eval (fts-only R@1).
	vals := make([]string, len(tokExprs))
	for i, e := range tokExprs {
		vals[i] = "(" + e + ")"
	}
	rows, err := c.db.QueryContext(ctx,
		`WITH d AS (
		   SELECT tok.q AS q,
		          (SELECT count(*) FROM memories x WHERE x.user_id = ? AND x.deleted_at IS NULL AND x.tsv @@ tok.q) AS n
		   FROM (VALUES `+strings.Join(vals, ", ")+`) AS tok(q)
		 ), tot AS (SELECT GREATEST(count(*), 1)::float8 AS c FROM memories WHERE user_id = ? AND deleted_at IS NULL)
		 SELECT m.id, m.content, m.source, m.speaker, m.confidence, m.created_at, m.updated_at, m.valid_at, m.invalid_at, m.expired_at, m.static, m.forget_after, COALESCE(m.written_by_connection_id, '')
		 FROM memories m JOIN d ON m.tsv @@ d.q CROSS JOIN tot
		 WHERE m.user_id = ? AND m.deleted_at IS NULL AND `+clause+`
		 GROUP BY m.id
		 ORDER BY sum(ln(1 + tot.c / GREATEST(d.n, 1))) / sqrt(GREATEST(array_length(tsvector_to_array(m.tsv), 1), 1)) DESC, m.id
		 LIMIT ?`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("cortex: search memories: %w", err)
	}
	defer rows.Close()

	var memories []Memory
	for rows.Next() {
		var m Memory
		var spk sql.NullString
		var vat, iat, eat, fa sql.NullTime
		if err := rows.Scan(&m.ID, &m.Content, &m.Source, &spk, &m.Confidence, &m.CreatedAt, &m.UpdatedAt, &vat, &iat, &eat, &m.Static, &fa, &m.WrittenBy); err != nil {
			return nil, fmt.Errorf("cortex: scan memory: %w", err)
		}
		if spk.Valid {
			m.Speaker = spk.String
		}
		m.ValidAt = nullTimePtr(vat)
		m.InvalidAt = nullTimePtr(iat)
		m.ExpiredAt = nullTimePtr(eat)
		m.ForgetAfter = nullTimePtr(fa)
		memories = append(memories, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cortex: iterate memories: %w", err)
	}

	// Load entity links for each memory.
	for i := range memories {
		entityIDs, err := c.loadMemoryEntityIDs(ctx, memories[i].ID)
		if err != nil {
			return nil, err
		}
		memories[i].EntityIDs = entityIDs
	}
	return memories, nil
}

// GetMemoriesByEntity returns all memories linked to the given entity.
// Entity links are loaded from memory_entities for each result.
func (c *Cortex) GetMemoriesByEntity(ctx context.Context, entityID string) ([]Memory, error) {
	return c.getMemoriesByEntityMode(ctx, entityID, model.TemporalMode{})
}

// getMemoriesByEntityMode is the temporal-mode-aware backing for
// GetMemoriesByEntity. The validity predicate is derived from mode
// (default = currently-valid).
func (c *Cortex) getMemoriesByEntityMode(ctx context.Context, entityID string, mode model.TemporalMode) ([]Memory, error) {
	if err := requireEntity(ctx, c.db, c.userID, entityID); err != nil {
		return nil, err
	}
	clause, targs := mode.Clause("m")
	// Arg order: user id, entity_id, then temporal args.
	args := []any{c.userID, entityID}
	args = append(args, targs...)

	rows, err := c.db.QueryContext(ctx,
		`SELECT m.id, m.content, m.source, m.speaker, m.confidence, m.created_at, m.updated_at, m.valid_at, m.invalid_at, m.expired_at, m.static, m.forget_after, COALESCE(m.written_by_connection_id, '')
		 FROM memories m
		 JOIN memory_entities me ON m.id = me.memory_id AND me.user_id = m.user_id
		 WHERE m.user_id = ? AND m.deleted_at IS NULL AND me.entity_id = ? AND `+clause,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("cortex: get memories by entity: %w", err)
	}
	defer rows.Close()

	var memories []Memory
	for rows.Next() {
		var m Memory
		var spk sql.NullString
		var vat, iat, eat, fa sql.NullTime
		if err := rows.Scan(&m.ID, &m.Content, &m.Source, &spk, &m.Confidence, &m.CreatedAt, &m.UpdatedAt, &vat, &iat, &eat, &m.Static, &fa, &m.WrittenBy); err != nil {
			return nil, fmt.Errorf("cortex: scan memory: %w", err)
		}
		if spk.Valid {
			m.Speaker = spk.String
		}
		m.ValidAt = nullTimePtr(vat)
		m.InvalidAt = nullTimePtr(iat)
		m.ExpiredAt = nullTimePtr(eat)
		m.ForgetAfter = nullTimePtr(fa)
		memories = append(memories, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cortex: iterate memories: %w", err)
	}

	// Load entity links for each memory.
	for i := range memories {
		entityIDs, err := c.loadMemoryEntityIDs(ctx, memories[i].ID)
		if err != nil {
			return nil, err
		}
		memories[i].EntityIDs = entityIDs
	}
	return memories, nil
}

// loadMemoryEntityIDs loads entity IDs linked to a memory from the
// memory_entities junction table.
func (c *Cortex) loadMemoryEntityIDs(ctx context.Context, memoryID string) ([]string, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT entity_id FROM memory_entities WHERE user_id = ? AND memory_id = ?`,
		c.userID, memoryID,
	)
	if err != nil {
		return nil, fmt.Errorf("cortex: load memory entity IDs: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("cortex: scan entity ID: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cortex: iterate entity IDs: %w", err)
	}
	return ids, nil
}

// orTSQuery turns an arbitrary user string into an OR-joined tsquery
// expression: each whitespace-separated token goes through plainto_tsquery
// (which never raises on punctuation and drops stopwords), and the results are
// OR-joined. The expression references explicit $n placeholders starting at
// firstArg; the returned args are the tokens in order. Returns "", nil for an
// all-whitespace input (callers treat that as no results).
func orTSQuery(q string, firstArg int) (expr string, args []any) {
	parts, args := tsTokenExprs(q, firstArg)
	if len(parts) == 0 {
		return "", nil
	}
	return "(" + strings.Join(parts, " || ") + ")", args
}

// tsTokenExprs returns one plainto_tsquery expression per whitespace-separated
// token (placeholders $firstArg, $firstArg+1, ...) and the matching args.
func tsTokenExprs(q string, firstArg int) (exprs []string, args []any) {
	fields := strings.Fields(q)
	exprs = make([]string, len(fields))
	args = make([]any, len(fields))
	for i, f := range fields {
		exprs[i] = fmt.Sprintf("plainto_tsquery('english', $%d)", firstArg+i)
		args[i] = f
	}
	return exprs, args
}

// InvalidateMemory soft-retires a memory: it sets expired_at to now (the
// system has retired this memory) and, when invalidAt is non-nil, sets
// invalid_at to the event time the fact stopped being true. The row, its
// embedding are left intact so the memory remains
// available to point-in-time history queries; default recall hides it via
// the currently-valid predicate. Returns an error if no memory has the
// given id. This is the primitive Phase 2b's reconciliation calls on
// supersession; it never deletes (use Forget for hard deletion).
func (c *Cortex) InvalidateMemory(ctx context.Context, id string, invalidAt *time.Time) error {
	now := time.Now().UTC()

	var res sql.Result
	var err error
	if invalidAt != nil {
		res, err = c.db.ExecContext(ctx,
			`UPDATE memories SET expired_at = ?, invalid_at = ?, updated_at = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL`,
			now, invalidAt.UTC(), now, id, c.userID,
		)
	} else {
		res, err = c.db.ExecContext(ctx,
			`UPDATE memories SET expired_at = ?, updated_at = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL`,
			now, now, id, c.userID,
		)
	}
	if err != nil {
		return fmt.Errorf("cortex: invalidate memory: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("cortex: invalidate memory rows affected: %w", err)
	}
	if n == 0 {
		return errMemoryNotFound()
	}
	return nil
}

// getMemoryByID loads a single memory by id regardless of validity state
// (no currently-valid filter), including its temporal fields. Returns
// (nil, nil) if no such memory exists. Used by reconciliation's apply-time
// re-validation, which must distinguish "missing" from "already invalidated".
func (c *Cortex) getMemoryByID(ctx context.Context, id string) (*Memory, error) {
	var m Memory
	var spk sql.NullString
	var vat, iat, eat, fa sql.NullTime
	err := c.db.QueryRowContext(ctx,
		`SELECT id, content, source, speaker, confidence, created_at, updated_at, valid_at, invalid_at, expired_at, static, forget_after, COALESCE(written_by_connection_id, '')
		 FROM memories WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, id, c.userID,
	).Scan(&m.ID, &m.Content, &m.Source, &spk, &m.Confidence, &m.CreatedAt, &m.UpdatedAt, &vat, &iat, &eat, &m.Static, &fa, &m.WrittenBy)
	if err == sql.ErrNoRows {
		return nil, errMemoryNotFound()
	}
	if err != nil {
		return nil, fmt.Errorf("cortex: get memory by id: %w", err)
	}
	if spk.Valid {
		m.Speaker = spk.String
	}
	m.ValidAt = nullTimePtr(vat)
	m.InvalidAt = nullTimePtr(iat)
	m.ExpiredAt = nullTimePtr(eat)
	m.ForgetAfter = nullTimePtr(fa)
	return &m, nil
}
