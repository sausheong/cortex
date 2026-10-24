// Package enctest opens per-test encrypted handles at the store level, for
// the row-path tests of internal/graph and internal/memory (which cannot use
// internal/testutil because it imports the root package).
package enctest

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/pgtest"
	"github.com/sausheong/cortex/internal/store"
	"github.com/sausheong/cortex/internal/tenancy"
)

// Env is a fresh schema with one user whose encryption_state is State.
type Env struct {
	T       testing.TB
	DB      *sql.DB
	Root    *store.Handle // bound to the bootstrap user, no key
	H       *store.Handle // the user's handle, holding Key
	Keyless *store.Handle // same user and state, no key
	UK      *keys.UK
	UserID  string
}

// Open creates the schema, migrates, creates a user in the given state
// ("none" or "encrypted") and returns a keyed handle for it.
func Open(t testing.TB, state string) *Env {
	t.Helper()
	dsn := pgtest.DSN(t)
	root, err := store.Connect(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	if err := root.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e := &Env{T: t, DB: db, Root: root}
	e.UserID = e.NewUser("enc-sub-"+state, state)
	uk, err := keys.NewUK()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(uk.Zero)
	e.UK = uk
	u := *root
	u.UserID = e.UserID
	e.H = u.WithKey(uk, state)
	t.Cleanup(e.H.Release)
	e.Keyless = u.WithKey(nil, state)
	return e
}

// NewUser creates another user in state and returns its id.
func (e *Env) NewUser(sub, state string) string {
	e.T.Helper()
	u, err := tenancy.New(e.Root).CreateUser(context.Background(), sub, sub+"@x.test", sub, true)
	if err != nil {
		e.T.Fatal(err)
	}
	if _, err := e.DB.Exec(`UPDATE users SET encryption_state = $1 WHERE id = $2`, state, u.ID); err != nil {
		e.T.Fatal(err)
	}
	return u.ID
}

// HandleFor returns a keyed handle bound to user (sharing this Env's UK).
func (e *Env) HandleFor(user, state string, uk *keys.UK) *store.Handle {
	u := *e.Root
	u.UserID = user
	h := u.WithKey(uk, state)
	e.T.Cleanup(h.Release)
	return h
}

// Exec runs SQL and fails the test on error.
func (e *Env) Exec(q string, args ...any) {
	e.T.Helper()
	if _, err := e.DB.Exec(q, args...); err != nil {
		e.T.Fatalf("exec %q: %v", q, err)
	}
}

// Count returns a single integer from q.
func (e *Env) Count(q string, args ...any) int {
	e.T.Helper()
	var n int
	if err := e.DB.QueryRow(q, args...).Scan(&n); err != nil {
		e.T.Fatalf("count %q: %v", q, err)
	}
	return n
}
