package cortex

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

// rebind rewrites '?' placeholders as Postgres $1..$n.
func rebind(q string) string {
	if !strings.Contains(q, "?") {
		return q
	}
	var b strings.Builder
	b.Grow(len(q) + 8)
	n := 0
	for i := 0; i < len(q); i++ {
		if q[i] == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(q[i])
	}
	return b.String()
}

// dbConn wraps *sql.DB so SQL written with '?' placeholders runs on Postgres.
type dbConn struct{ *sql.DB }

func (d dbConn) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return d.DB.ExecContext(ctx, rebind(q), args...)
}

func (d dbConn) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return d.DB.QueryContext(ctx, rebind(q), args...)
}

func (d dbConn) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return d.DB.QueryRowContext(ctx, rebind(q), args...)
}

func (d dbConn) BeginTx(ctx context.Context, opts *sql.TxOptions) (*dbTx, error) {
	tx, err := d.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &dbTx{tx}, nil
}

// dbTx wraps *sql.Tx with the same placeholder rewriting.
type dbTx struct{ *sql.Tx }

func (t *dbTx) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return t.Tx.ExecContext(ctx, rebind(q), args...)
}

func (t *dbTx) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return t.Tx.QueryContext(ctx, rebind(q), args...)
}

func (t *dbTx) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return t.Tx.QueryRowContext(ctx, rebind(q), args...)
}
