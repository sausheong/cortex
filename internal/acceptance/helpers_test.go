// Package acceptance holds the end-to-end acceptance tests for per-user
// encryption. They are thin: each drives the owning packages through their
// exported APIs (and HTTP where a flow lives in a handler) and asserts the
// outcome the spec's Acceptance list names. A failure here is a defect in the
// owning task and is fixed there with a regression test.
package acceptance

import (
	"context"
	"database/sql"
	"hash/fnv"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/pgtest"
	"github.com/sausheong/cortex/internal/webauthntest"
)

const (
	rpID   = "cortex.test"
	origin = "https://cortex.test"
)

type env struct {
	t    *testing.T
	dsn  string
	root *cortex.Cortex
	db   *sql.DB
	st   *accounts.Store
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dsn := pgtest.DSN(t)
	root, err := cortex.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e := &env{t: t, dsn: dsn, root: root, db: db, st: &accounts.Store{DB: db}}
	if err := e.st.ConfigurePasskeys(accounts.PasskeyConfig{RPID: rpID, Origins: []string{origin}, EnrollEnabled: true}); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) user(sub string) string {
	e.t.Helper()
	u, err := e.root.CreateUser(context.Background(), sub, sub+"@x.test", sub, true)
	if err != nil {
		e.t.Fatal(err)
	}
	return u.ID
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.db.Exec(q, args...); err != nil {
		e.t.Fatalf("exec %q: %v", q, err)
	}
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatalf("count %q: %v", q, err)
	}
	return n
}

func (e *env) state(uid string) string {
	e.t.Helper()
	var s string
	if err := e.db.QueryRow(`SELECT encryption_state FROM users WHERE id = $1`, uid).Scan(&s); err != nil {
		e.t.Fatal(err)
	}
	return s
}

// enrolled is the outcome of a full enrollment.
type enrolled struct {
	Dev   *webauthntest.Authenticator
	Res   *accounts.EnrollResult
	PRF   []byte
	State string
}

func groupsFor(res *accounts.EnrollResult) []string {
	g := strings.Split(res.Recovery, "-")
	return []string{g[res.Positions[0]-1], g[res.Positions[1]-1]}
}

// enroll runs the whole passkey enrollment ceremony for uid (begin, finish,
// confirm with the recovery proof). Res.UK is the caller's to Zero.
func (e *env) enroll(uid string) *enrolled {
	e.t.Helper()
	ctx := context.Background()
	dev := webauthntest.New(e.t, rpID, origin)
	c, err := e.st.BeginEnroll(ctx, uid)
	if err != nil {
		e.t.Fatal(err)
	}
	prf := dev.PRFFromCreate(e.t, c.Options)
	res, err := e.st.FinishEnroll(ctx, uid, c.ID, dev.Create(e.t, c.Options), prf, "Laptop")
	if err != nil {
		e.t.Fatal(err)
	}
	cc, err := e.st.BeginConfirm(ctx, uid, res.ConfirmID)
	if err != nil {
		e.t.Fatal(err)
	}
	assertion, prf2 := dev.Get(e.t, cc.Options)
	state, uk, err := e.st.ConfirmEnroll(ctx, uid, res.ConfirmID, assertion, prf2, groupsFor(res))
	if err != nil {
		e.t.Fatal(err)
	}
	uk.Zero()
	e.t.Cleanup(res.UK.Zero)
	return &enrolled{Dev: dev, Res: res, PRF: prf, State: state}
}

// unlock runs a passkey unlock ceremony; prfOverride replaces the PRF output.
func (e *env) unlock(uid string, dev *webauthntest.Authenticator, prfOverride []byte) (*keys.UK, error) {
	e.t.Helper()
	ctx := context.Background()
	c, err := e.st.BeginUnlock(ctx, uid)
	if err != nil {
		return nil, err
	}
	assertion, prf := dev.Get(e.t, c.Options)
	if prfOverride != nil {
		prf = prfOverride
	}
	return e.st.FinishUnlock(ctx, uid, c.ID, assertion, prf)
}

// keyed returns a handle for uid holding uk in state.
func (e *env) keyed(uid string, uk *keys.UK, state string) *cortex.Cortex {
	e.t.Helper()
	cx := e.root.ForUser(uid, cortex.WithEmbedder(bagEmbedder{dims: 512})).WithKey(uk, state)
	e.t.Cleanup(cx.Release)
	return cx
}

// dump renders every row of every table in the test schema as lowercase
// JSON text (bytea as lowercase hex), the way a database dump would expose it.
func (e *env) dump() string {
	e.t.Helper()
	rows, err := e.db.Query(`SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_type = 'BASE TABLE' ORDER BY 1`)
	if err != nil {
		e.t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	var b strings.Builder
	for _, tb := range tables {
		r, err := e.db.Query(`SELECT to_jsonb(t)::text FROM "` + tb + `" t`)
		if err != nil {
			e.t.Fatalf("dump %s: %v", tb, err)
		}
		for r.Next() {
			var s string
			r.Scan(&s)
			b.WriteString(tb)
			b.WriteString(": ")
			b.WriteString(strings.ToLower(s))
			b.WriteString("\n")
		}
		r.Close()
	}
	return b.String()
}

// bagEmbedder is a deterministic bag-of-words embedder for tests.
type bagEmbedder struct{ dims int }

func (b bagEmbedder) Dimensions() int { return b.dims }
func (b bagEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, b.dims)
		for _, w := range strings.Fields(strings.ToLower(t)) {
			h := fnv.New32a()
			h.Write([]byte(w))
			v[int(h.Sum32())%b.dims]++
		}
		out[i] = v
	}
	return out, nil
}
