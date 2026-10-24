package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"sync"
)

// Rebind rewrites '?' placeholders as Postgres $1..$n.
func Rebind(q string) string {
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

// OnExec, when set, is told about every statement that writes (INSERT,
// UPDATE or DELETE, including INSERT ... RETURNING through a query): outside a
// transaction right after it ran, inside one when the transaction commits
// (a rolled-back write is never reported). The working set uses it as a
// catch-all so no write path can leave a stale decrypted cache behind. It may
// be nil.
var OnExec func(q string, args []any)

// OnQuery, when set, sees the SQL text and arguments of every statement before it runs. It
// exists for tests that prove which statements a code path issues; it is nil
// in production.
var OnQuery func(q string, args []any)

func seen(q string, args []any) {
	if f := OnQuery; f != nil {
		f(q, args)
	}
}

// isWrite reports whether q modifies rows. Reads never match.
func isWrite(q string) bool {
	s := strings.TrimLeft(q, " \t\r\n")
	if len(s) < 6 {
		return false
	}
	switch strings.ToUpper(s[:6]) {
	case "INSERT", "UPDATE", "DELETE":
		return true
	}
	if strings.EqualFold(s[:4], "with") {
		u := strings.ToUpper(q)
		return strings.Contains(u, "INSERT INTO") || strings.Contains(u, "UPDATE ") || strings.Contains(u, "DELETE FROM")
	}
	return false
}

func reportExec(q string, args []any) {
	if f := OnExec; f != nil && isWrite(q) {
		f(q, args)
	}
}

// DB wraps *sql.DB so SQL written with '?' placeholders runs on Postgres.
type DB struct{ *sql.DB }

func (d DB) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	seen(q, args)
	res, err := d.DB.ExecContext(ctx, Rebind(q), args...)
	if err == nil {
		reportExec(q, args)
	}
	return res, err
}

func (d DB) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	seen(q, args)
	rows, err := d.DB.QueryContext(ctx, Rebind(q), args...)
	if err == nil {
		reportExec(q, args)
	}
	return rows, err
}

func (d DB) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	seen(q, args)
	row := d.DB.QueryRowContext(ctx, Rebind(q), args...)
	reportExec(q, args)
	return row
}

func (d DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*Tx, error) {
	tx, err := d.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &Tx{Tx: tx}, nil
}

// Tx wraps *sql.Tx with the same placeholder rewriting. Writes are reported
// to OnExec when the transaction commits.
type Tx struct {
	*sql.Tx
	mu      sync.Mutex
	pending []pendingExec
}

type pendingExec struct {
	q    string
	args []any
}

func (t *Tx) note(q string, args []any) {
	if OnExec == nil || !isWrite(q) {
		return
	}
	t.mu.Lock()
	t.pending = append(t.pending, pendingExec{q, append([]any(nil), args...)})
	t.mu.Unlock()
}

func (t *Tx) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	seen(q, args)
	res, err := t.Tx.ExecContext(ctx, Rebind(q), args...)
	if err == nil {
		t.note(q, args)
	}
	return res, err
}

func (t *Tx) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	seen(q, args)
	rows, err := t.Tx.QueryContext(ctx, Rebind(q), args...)
	if err == nil {
		t.note(q, args)
	}
	return rows, err
}

func (t *Tx) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	seen(q, args)
	row := t.Tx.QueryRowContext(ctx, Rebind(q), args...)
	t.note(q, args)
	return row
}

// Commit commits and then reports the transaction's writes.
func (t *Tx) Commit() error {
	if err := t.Tx.Commit(); err != nil {
		return err
	}
	t.mu.Lock()
	p := t.pending
	t.pending = nil
	t.mu.Unlock()
	for _, e := range p {
		reportExec(e.q, e.args)
	}
	return nil
}
