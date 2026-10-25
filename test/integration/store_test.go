package integration_test

import (
	"context"
	"strings"
	"testing"
	"time"

	. "github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/testutil"

	"github.com/sausheong/cortex/internal/pgtest"
)

func TestOpenAppliesMigrations(t *testing.T) {
	c, err := Open(pgtest.DSN(t))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer c.Close()
	if v := schemaVersion(t, c); v != 13 {
		t.Errorf("version = %d, want 13", v)
	}
	var n int
	err = testutil.H(c).DB.QueryRowContext(context.Background(),
		`SELECT count(*) FROM information_schema.columns
		 WHERE table_schema = current_schema() AND table_name = 'memories' AND column_name = 'tsv'`).Scan(&n)
	if err != nil || n != 0 {
		t.Errorf("memories.tsv should be gone (n=%d err=%v)", n, err)
	}
}

func TestOpenIdempotent(t *testing.T) {
	dsn := pgtest.DSN(t)
	c1, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	c1.Close()
	c2, err := Open(dsn)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer c2.Close()
	if v := schemaVersion(t, c2); v != 13 {
		t.Errorf("version = %d, want 13", v)
	}
}

func TestOpenUnreachable(t *testing.T) {
	start := time.Now()
	_, err := Open("postgres://u:p@127.0.0.1:1/x?sslmode=disable")
	if err == nil {
		t.Fatal("expected error")
	}
	if time.Since(start) > 6*time.Second {
		t.Errorf("took %v", time.Since(start))
	}
	if !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("error should name host:port, got %v", err)
	}
}

func TestSyncStateMultipleConnectors(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	if err := c.SetSyncState(ctx, "a", "1"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetSyncState(ctx, "b", "2"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetSyncState(ctx, "a", "3"); err != nil {
		t.Fatal(err)
	}
	if s, _ := c.GetSyncState(ctx, "a"); s != "3" {
		t.Errorf("a = %q", s)
	}
	if s, _ := c.GetSyncState(ctx, "b"); s != "2" {
		t.Errorf("b = %q", s)
	}
}
