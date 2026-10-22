// Package pgtest provides per-test isolated Postgres schemas. It imports
// nothing from cortex so package cortex tests can use it without a cycle.
package pgtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// DSN creates an isolated schema in the database named by
// CORTEX_TEST_DATABASE_URL and returns a DSN whose search_path points at it
// (plus public, where pgvector lives). The schema is dropped on cleanup.
// The test is skipped if the env var is unset.
func DSN(t testing.TB) string {
	t.Helper()
	base := getenv("CORTEX_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set CORTEX_TEST_DATABASE_URL (make test-db) to run DB tests")
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("pgtest: rand: %v", err)
	}
	schema := "t_" + hex.EncodeToString(b[:])

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatalf("pgtest: open: %v", err)
	}
	defer admin.Close()
	if _, err := admin.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
		t.Fatalf("pgtest: create extension: %v", err)
	}
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("pgtest: create schema: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		db, err := sql.Open("pgx", base)
		if err != nil {
			return
		}
		defer db.Close()
		_, _ = db.ExecContext(ctx, `DROP SCHEMA `+schema+` CASCADE`)
	})

	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("pgtest: parse url: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema+",public")
	u.RawQuery = q.Encode()
	return u.String()
}

func getenv(k string) string { return os.Getenv(k) }
