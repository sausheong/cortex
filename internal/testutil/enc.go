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

// ModeCounts reports how many times each ForEachMode arm ("encrypted") has
// run in this process.
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
// CreateUser writes no owner entity (enrollment creates it sealed), so the
// returned account starts empty. Search, filters and listings work for the
// returned handle.
func OpenEncrypted(t testing.TB) (*cortex.Cortex, *keys.UK) {
	t.Helper()
	return OpenEncryptedWith(t)
}

// OpenEncryptedWith is OpenEncrypted with options for the opened instance.
func OpenEncryptedWith(t testing.TB, opts ...cortex.Option) (*cortex.Cortex, *keys.UK) {
	t.Helper()
	dsn := pgtest.DSN(t)
	root, err := cortex.Open(dsn, opts...)
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

// ForEachMode runs fn once against an encrypted handle, as subtest
// "encrypted". Plaintext accounts no longer exist (migration 012), so the
// legacy arm is gone; the helper keeps its name so call sites read the same.
// The count is recorded for the coverage check. Tests inside package cortex
// cannot import this package (import cycle) and use their own forEachMode.
func ForEachMode(t *testing.T, fn func(*testing.T, *cortex.Cortex)) {
	t.Helper()
	t.Run("encrypted", func(t *testing.T) {
		cx, _ := OpenEncrypted(t)
		modeMu.Lock()
		modeCounts["encrypted"]++
		modeMu.Unlock()
		fn(t, cx)
	})
}
