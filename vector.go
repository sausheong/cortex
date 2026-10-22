package cortex

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// defaultEmbeddingDims is used when no embedder is configured at first Open.
const defaultEmbeddingDims = 512

// checkPgvectorVersion returns an error if v is older than 0.8.0.
func checkPgvectorVersion(v string) error {
	parts := strings.Split(strings.TrimSpace(v), ".")
	nums := make([]int, 3)
	for i := 0; i < 3 && i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return fmt.Errorf("cortex: can't parse pgvector version %q", v)
		}
		nums[i] = n
	}
	if nums[0] > 0 || nums[1] >= 8 {
		return nil
	}
	return fmt.Errorf("cortex: pgvector %s is too old, need >= 0.8.0 (ALTER EXTENSION vector UPDATE)", v)
}

// ensureVectorSchema checks the pgvector version and, on first Open, creates
// the dimension-dependent embedding column and partial HNSW indexes.
func (c *Cortex) ensureVectorSchema(ctx context.Context) error {
	var ver string
	if err := c.db.QueryRowContext(ctx,
		`SELECT extversion FROM pg_extension WHERE extname = 'vector'`).Scan(&ver); err != nil {
		return fmt.Errorf("cortex: pgvector extension not found: %w", err)
	}
	if err := checkPgvectorVersion(ver); err != nil {
		return err
	}

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cortex: begin vector setup: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(4243)`); err != nil {
		return fmt.Errorf("cortex: vector setup lock: %w", err)
	}

	var stored string
	err = tx.QueryRowContext(ctx, `SELECT value FROM cortex_meta WHERE key = 'embedding_dims'`).Scan(&stored)
	switch {
	case err == nil:
		n, perr := strconv.Atoi(stored)
		if perr != nil {
			return fmt.Errorf("cortex: bad stored embedding_dims %q", stored)
		}
		c.dims = n
		if c.cfg.embedder != nil && c.cfg.embedder.Dimensions() != n {
			return fmt.Errorf("cortex: embedder dimension %d does not match stored dimension %d", c.cfg.embedder.Dimensions(), n)
		}
		return nil
	case !isNoRows(err):
		return fmt.Errorf("cortex: read embedding_dims: %w", err)
	}

	n := defaultEmbeddingDims
	if c.cfg.embedder != nil {
		n = c.cfg.embedder.Dimensions()
	}
	if n <= 0 {
		return fmt.Errorf("cortex: invalid embedder dimension %d", n)
	}
	stmts := []string{
		fmt.Sprintf(`ALTER TABLE embeddings ADD COLUMN embedding halfvec(%d) NOT NULL`, n),
	}
	for _, rt := range []string{"entity", "memory", "chunk"} {
		stmts = append(stmts, fmt.Sprintf(
			`CREATE INDEX idx_embeddings_hnsw_%s ON embeddings USING hnsw (embedding halfvec_cosine_ops) WHERE ref_type = '%s'`, rt, rt))
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("cortex: vector schema: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO cortex_meta (key, value) VALUES ('embedding_dims', ?)`, strconv.Itoa(n)); err != nil {
		return fmt.Errorf("cortex: record embedding_dims: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cortex: commit vector setup: %w", err)
	}
	c.dims = n
	return nil
}

// vectorLiteral renders v in pgvector text form, e.g. [0.1,0.2].
func vectorLiteral(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// parseVectorLiteral parses pgvector text form into float32s.
func parseVectorLiteral(s string) ([]float32, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]float32, len(parts))
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 32)
		if err != nil {
			return nil, err
		}
		out[i] = float32(f)
	}
	return out, nil
}
