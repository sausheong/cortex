// Package pgtest provides per-test isolated Postgres schemas. It imports
// nothing from cortex so package cortex tests can use it without a cycle.
package pgtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
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
	dsn, cleanup, err := NewSchema(base)
	if err != nil {
		t.Fatalf("pgtest: %v", err)
	}
	t.Cleanup(cleanup)
	return dsn
}

// ExampleDSN is like DSN for Example functions, which have no testing.TB.
// ok is false when CORTEX_TEST_DATABASE_URL is unset or setup fails.
func ExampleDSN() (dsn string, cleanup func(), ok bool) {
	base := getenv("CORTEX_TEST_DATABASE_URL")
	if base == "" {
		return "", func() {}, false
	}
	dsn, cleanup, err := NewSchema(base)
	if err != nil {
		return "", func() {}, false
	}
	return dsn, cleanup, true
}

// NewSchema creates an isolated schema in the database at base and returns a
// DSN pointing at it plus a cleanup func that drops the schema.
func NewSchema(base string) (string, func(), error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", nil, fmt.Errorf("rand: %w", err)
	}
	schema := "t_" + hex.EncodeToString(b[:])

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := sql.Open("pgx", base)
	if err != nil {
		return "", nil, fmt.Errorf("open: %w", err)
	}
	defer admin.Close()
	if _, err := admin.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
		return "", nil, fmt.Errorf("create extension: %w", err)
	}
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		return "", nil, fmt.Errorf("create schema: %w", err)
	}
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		db, err := sql.Open("pgx", base)
		if err != nil {
			return
		}
		defer db.Close()
		_, _ = db.ExecContext(ctx, `DROP SCHEMA `+schema+` CASCADE`)
	}

	u, err := url.Parse(base)
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("parse url: %w", err)
	}
	q := u.Query()
	q.Set("search_path", schema+",public")
	u.RawQuery = q.Encode()
	return u.String(), cleanup, nil
}

func getenv(k string) string { return os.Getenv(k) }
