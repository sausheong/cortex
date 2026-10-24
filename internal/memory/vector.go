package memory

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/sausheong/cortex/internal/model"
)

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

// EnsureVectorSchema checks the pgvector version. The embedding column and
// its indexes are managed per user by migration 002 and the vector layer.
func (c Ops) EnsureVectorSchema(ctx context.Context) error {
	var ver string
	if err := c.DB.QueryRowContext(ctx,
		`SELECT extversion FROM pg_extension WHERE extname = 'vector'`).Scan(&ver); err != nil {
		return fmt.Errorf("cortex: pgvector extension not found: %w", err)
	}
	return checkPgvectorVersion(ver)
}

// VectorLiteral renders v in pgvector text form, e.g. [0.1,0.2].
func VectorLiteral(v []float32) string {
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

// ParseVectorLiteral parses pgvector text form into float32s.
func ParseVectorLiteral(s string) ([]float32, error) {
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

const ReembedLockSQL = `hashtextextended('reembed:' || ?::text, 0)`

// ActiveGeneration returns the user's active embedding generation.
func (c Ops) ActiveGeneration(ctx context.Context) (int, error) {
	var g int
	err := c.DB.QueryRowContext(ctx,
		`SELECT active_embedding_generation FROM users WHERE id = ?`, c.UserID).Scan(&g)
	if err == sql.ErrNoRows {
		return 0, model.NotFound("cortex: user not found")
	}
	if err != nil {
		return 0, fmt.Errorf("cortex: active generation: %w", err)
	}
	return g, nil
}

// SetActiveGeneration sets the user's active embedding generation.
func (c Ops) SetActiveGeneration(ctx context.Context, g int) error {
	res, err := c.DB.ExecContext(ctx,
		`UPDATE users SET active_embedding_generation = ? WHERE id = ?`, g, c.UserID)
	if err != nil {
		return fmt.Errorf("cortex: set active generation: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.NotFound("cortex: user not found")
	}
	return nil
}

func checkDims(n int) error {
	if n < 1 || n > 4000 {
		return fmt.Errorf("cortex: embedding has %d dims, want 1..4000", n)
	}
	return nil
}

// upsertEmbedding writes one embedding row through q.
func upsertEmbeddingSQL() string {
	return `INSERT INTO embeddings (id, user_id, ref_id, ref_type, embedding, dims, generation, created_at)
		 VALUES (?, ?, ?, ?, ?::halfvec, ?, ?, now())
		 ON CONFLICT (user_id, ref_type, ref_id, generation) DO UPDATE SET
		   embedding = EXCLUDED.embedding, dims = EXCLUDED.dims`
}

// PutEmbedding stores an embedding for the handle's generation (pinned, or
// the active one) under the shared per-user re-embed lock.
func (c Ops) PutEmbedding(ctx context.Context, refID, refType string, v []float32) error {
	return c.putEmbeddingGen(ctx, refID, refType, v, false, 0)
}

// PutEmbeddingGen is the re-embed writer. It accepts only gen equal to the
// active generation or active+1, and never applies the stale-pin check.
func (c Ops) PutEmbeddingGen(ctx context.Context, refID, refType string, gen int, v []float32) error {
	return c.putEmbeddingGen(ctx, refID, refType, v, true, gen)
}

func (c Ops) putEmbeddingGen(ctx context.Context, refID, refType string, v []float32, explicit bool, gen int) error {
	if err := checkDims(len(v)); err != nil {
		return err
	}
	if err := c.RequireKey(); err != nil {
		return err
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cortex: begin embedding write: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock_shared(`+ReembedLockSQL+`)`, c.UserID); err != nil {
		return fmt.Errorf("cortex: embedding lock: %w", err)
	}
	var active int
	if err := tx.QueryRowContext(ctx,
		`SELECT active_embedding_generation FROM users WHERE id = ?`, c.UserID).Scan(&active); err != nil {
		return fmt.Errorf("cortex: active generation: %w", err)
	}
	switch {
	case explicit:
		if gen != active && gen != active+1 {
			return fmt.Errorf("cortex: generation %d not allowed (active %d)", gen, active)
		}
	case c.PinnedGen > 0:
		if c.PinnedGen != active {
			return model.ErrStaleGeneration
		}
		gen = c.PinnedGen
	default:
		gen = active
	}
	if err := c.writeEmbedding(ctx, tx, refID, refType, gen, v); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	c.NotifyWrite("embedding")
	return nil
}

// FlipGeneration switches the user to newGen in one transaction under the
// exclusive re-embed lock. embedTail embeds rows still lacking newGen using
// the supplied put (same tx, no second lock); other generations' rows are
// then deleted and inTx runs before commit. Any error rolls everything back.
func (c Ops) FlipGeneration(ctx context.Context, newGen int,
	embedTail func(ctx context.Context, tx *sql.Tx, put func(refID, refType string, v []float32) error) error,
	inTx func(tx *sql.Tx) error) error {
	if newGen < 1 {
		return fmt.Errorf("cortex: invalid generation %d", newGen)
	}
	if err := c.RequireKey(); err != nil {
		return err
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cortex: begin flip: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(`+ReembedLockSQL+`)`, c.UserID); err != nil {
		return fmt.Errorf("cortex: flip lock: %w", err)
	}
	if embedTail != nil {
		put := func(refID, refType string, v []float32) error {
			if err := checkDims(len(v)); err != nil {
				return err
			}
			return c.writeEmbedding(ctx, tx, refID, refType, newGen, v)
		}
		if err := embedTail(ctx, tx.Tx, put); err != nil {
			return err
		}
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE users SET active_embedding_generation = ? WHERE id = ?`, newGen, c.UserID)
	if err != nil {
		return fmt.Errorf("cortex: flip generation: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.NotFound("cortex: user not found")
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM embeddings WHERE user_id = ? AND generation <> ?`, c.UserID, newGen); err != nil {
		return fmt.Errorf("cortex: drop old generations: %w", err)
	}
	if inTx != nil {
		if err := inTx(tx.Tx); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	c.NotifyWrite("embedding")
	return nil
}

// EnsureVectorIndex creates the partial HNSW index for (dims, refType) if it
// is missing, outside any transaction, serialised by an advisory lock. It
// must not be called on a request path.
func (c Ops) EnsureVectorIndex(ctx context.Context, dims int, refType string) error {
	if err := checkDims(dims); err != nil {
		return err
	}
	switch refType {
	case "entity", "memory", "chunk":
	default:
		return fmt.Errorf("cortex: unknown ref type %q", refType)
	}
	conn, err := c.DB.DB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("cortex: index conn: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(4243)`); err != nil {
		return fmt.Errorf("cortex: index lock: %w", err)
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(4243)`)
	q := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS emb_hnsw_%s_%d ON embeddings USING hnsw ((embedding::halfvec(%d)) halfvec_cosine_ops) WHERE dims = %d AND ref_type = '%s'`,
		refType, dims, dims, dims, refType)
	if _, err := conn.ExecContext(ctx, q); err != nil {
		return fmt.Errorf("cortex: create vector index: %w", err)
	}
	return nil
}
