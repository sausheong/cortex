package accounts

import (
	"database/sql"
	"testing"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/pgtest"
)

type env struct {
	st   *Store
	root *cortex.Cortex
	now  time.Time
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
	e := &env{root: root, now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	e.st = &Store{DB: db, Now: func() time.Time { return e.now }}
	return e
}

func (e *env) user(t *testing.T, sub string) string {
	t.Helper()
	u, err := e.root.CreateUser(t.Context(), sub, sub+"@x.test", sub, true)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// encUser creates a user in state "encrypted" and returns its id and key.
func (e *env) encUser(t *testing.T, sub string) (string, *keys.UK) {
	t.Helper()
	id := e.user(t, sub)
	if _, err := e.st.DB.Exec(`UPDATE users SET encryption_state = 'encrypted' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	uk, err := keys.NewUK()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(uk.Zero)
	return id, uk
}

func okSettings() Settings {
	return Settings{BaseURL: "https://api.example.com/v1", APIKey: "sk-secret-3f9a", ChatModel: "m", EmbedModel: "e", EmbedDims: 512}
}
