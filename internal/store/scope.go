package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/sausheong/cortex/internal/model"
)

// RowQuerier is satisfied by DB and *Tx.
type RowQuerier interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

// ErrEntityNotFound and ErrMemoryNotFound carry no ID, so a foreign ID and an
// absent ID produce byte-identical errors.
func ErrEntityNotFound() error { return model.NotFound("cortex: entity not found") }
func ErrMemoryNotFound() error { return model.NotFound("cortex: memory not found") }

// RequireEntity returns ErrNotFound unless a live entity with this ID belongs
// to userID.
func RequireEntity(ctx context.Context, q RowQuerier, userID, id string) error {
	var one int
	err := q.QueryRowContext(ctx,
		`SELECT 1 FROM entities WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, id, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrEntityNotFound()
	}
	if err != nil {
		return fmt.Errorf("cortex: check entity: %w", err)
	}
	return nil
}

// RequireMemory returns ErrNotFound unless a live memory with this ID belongs
// to userID.
func RequireMemory(ctx context.Context, q RowQuerier, userID, id string) error {
	var one int
	err := q.QueryRowContext(ctx,
		`SELECT 1 FROM memories WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, id, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrMemoryNotFound()
	}
	if err != nil {
		return fmt.Errorf("cortex: check memory: %w", err)
	}
	return nil
}
