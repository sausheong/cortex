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

// EnsureVectorSchema checks the pgvector version. Embeddings are stored as
// ciphertext and searched over the decrypted working set, so no vector index
// exists; the extension is only needed for the dims bookkeeping column type.
func (c Ops) EnsureVectorSchema(ctx context.Context) error {
	var ver string
	if err := c.DB.QueryRowContext(ctx,
		`SELECT extversion FROM pg_extension WHERE extname = 'vector'`).Scan(&ver); err != nil {
		return fmt.Errorf("cortex: pgvector extension not found: %w", err)
	}
	return checkPgvectorVersion(ver)
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
