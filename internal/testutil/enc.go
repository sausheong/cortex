package testutil

import (
	"database/sql"
	"sync"
	"testing"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/pgtest"
)

var (
	modeMu     sync.Mutex
	modeCounts = map[string]int{}
)

// ModeCounts reports how many times each ForEachMode arm ("legacy",
// "encrypted") has run in this process.
func ModeCounts() map[string]int {
	modeMu.Lock()
	defer modeMu.Unlock()
	out := map[string]int{}
	for k, v := range modeCounts {
		out[k] = v
	}
	return out
}

// OpenEncrypted opens a fresh per-test schema and returns a handle bound to a
// new user whose encryption_state is 'encrypted', carrying its own clone of
// the returned user key.
//
// NOTE: encrypted rows are not meaningful for search until Tasks 8 and 12
// land. Until then use this (and ForEachMode's encrypted arm) only in the
// helpers' own tests and key-plumbing tests.
func OpenEncrypted(t testing.TB) (*cortex.Cortex, *keys.UK) {
	t.Helper()
	dsn := pgtest.DSN(t)
	root, err := cortex.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	u, err := root.CreateUser(t.Context(), "enc-sub", "enc@x.test", "enc", true)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE users SET encryption_state = 'encrypted' WHERE id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}
	uk, err := keys.NewUK()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(uk.Zero)
	cx := root.ForUser(u.ID).WithKey(uk, "encrypted")
	t.Cleanup(cx.Release)
	return cx, uk
}

// ForEachMode runs fn once against a legacy (unencrypted) handle and once
// against an encrypted one, as subtests "legacy" and "encrypted". The counts
// are recorded for the Task 18 coverage check. Same caveat as OpenEncrypted:
// do not use it for search-dependent tests before Tasks 8 and 12.
func ForEachMode(t *testing.T, fn func(*testing.T, *cortex.Cortex)) {
	t.Helper()
	t.Run("legacy", func(t *testing.T) {
		root, err := cortex.Open(pgtest.DSN(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { root.Close() })
		modeMu.Lock()
		modeCounts["legacy"]++
		modeMu.Unlock()
		fn(t, root)
	})
	t.Run("encrypted", func(t *testing.T) {
		cx, _ := OpenEncrypted(t)
		modeMu.Lock()
		modeCounts["encrypted"]++
		modeMu.Unlock()
		fn(t, cx)
	})
}
