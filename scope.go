package cortex

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/sausheong/cortex/internal/model"
)

// rowQuerier is satisfied by dbConn and *dbTx.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

// errEntityNotFound and errMemoryNotFound carry no ID, so a foreign ID and an
// absent ID produce byte-identical errors.
func errEntityNotFound() error { return model.NotFound("cortex: entity not found") }
func errMemoryNotFound() error { return model.NotFound("cortex: memory not found") }

// requireEntity returns ErrNotFound unless a live entity with this ID belongs
// to userID.
func requireEntity(ctx context.Context, q rowQuerier, userID, id string) error {
	var one int
	err := q.QueryRowContext(ctx,
		`SELECT 1 FROM entities WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, id, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return errEntityNotFound()
	}
	if err != nil {
		return fmt.Errorf("cortex: check entity: %w", err)
	}
	return nil
}

// requireMemory returns ErrNotFound unless a live memory with this ID belongs
// to userID.
func requireMemory(ctx context.Context, q rowQuerier, userID, id string) error {
	var one int
	err := q.QueryRowContext(ctx,
		`SELECT 1 FROM memories WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, id, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return errMemoryNotFound()
	}
	if err != nil {
		return fmt.Errorf("cortex: check memory: %w", err)
	}
	return nil
}
