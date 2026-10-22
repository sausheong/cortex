package accounts

import (
	"database/sql"
	"testing"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/pgtest"
)

const testKeyHex = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

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
	sl, err := NewSealer(testKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{root: root, now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	e.st = &Store{DB: db, Sealer: sl, Now: func() time.Time { return e.now }}
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

func okSettings() Settings {
	return Settings{BaseURL: "https://api.example.com/v1", APIKey: "sk-secret-3f9a", ChatModel: "m", EmbedModel: "e", EmbedDims: 512}
}
