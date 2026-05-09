package cortex

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenCreatesDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	c, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer c.Close()

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatal("expected database file to be created")
	}
}

func TestOpenCreatesTablesAndIndexes(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	c, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer c.Close()

	expectedTables := []string{
		"entities",
		"relationships",
		"chunks",
		"memories",
		"memory_entities",
		"embeddings",
		"sync_state",
	}

	for _, table := range expectedTables {
		var name string
		err := c.db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", table, err)
		}
	}
}

func TestOpenExistingDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// First open — creates the DB.
	c1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first Open() error: %v", err)
	}
	c1.Close()

	// Second open — reuses existing DB.
	c2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second Open() error: %v", err)
	}
	defer c2.Close()
}

func TestSyncStateMultipleConnectors(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	c, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer c.Close()

	ctx := context.Background()

	if err := c.SetSyncState(ctx, "connector-a", "state-A"); err != nil {
		t.Fatalf("SetSyncState(a): %v", err)
	}
	if err := c.SetSyncState(ctx, "connector-b", "state-B"); err != nil {
		t.Fatalf("SetSyncState(b): %v", err)
	}

	stateA, err := c.GetSyncState(ctx, "connector-a")
	if err != nil {
		t.Fatalf("GetSyncState(a): %v", err)
	}
	if stateA != "state-A" {
		t.Errorf("connector-a: got %q, want %q", stateA, "state-A")
	}

	stateB, err := c.GetSyncState(ctx, "connector-b")
	if err != nil {
		t.Fatalf("GetSyncState(b): %v", err)
	}
	if stateB != "state-B" {
		t.Errorf("connector-b: got %q, want %q", stateB, "state-B")
	}

	// Update A without affecting B.
	if err := c.SetSyncState(ctx, "connector-a", "state-A2"); err != nil {
		t.Fatalf("SetSyncState(a update): %v", err)
	}
	stateB2, _ := c.GetSyncState(ctx, "connector-b")
	if stateB2 != "state-B" {
		t.Errorf("connector-b changed unexpectedly: got %q", stateB2)
	}
}

func TestOpenNestedDirectory(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "deep", "nested", "dir", "test.db")

	c, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() nested directory error: %v", err)
	}
	defer c.Close()

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatal("expected database file to be created in nested directory")
	}
}

func TestSyncState(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	c, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer c.Close()

	ctx := context.Background()

	// Initially empty.
	state, err := c.GetSyncState(ctx, "markdown")
	if err != nil {
		t.Fatalf("GetSyncState() error: %v", err)
	}
	if state != "" {
		t.Fatalf("expected empty state, got %q", state)
	}

	// Set state.
	if err := c.SetSyncState(ctx, "markdown", "cursor-123"); err != nil {
		t.Fatalf("SetSyncState() error: %v", err)
	}

	state, err = c.GetSyncState(ctx, "markdown")
	if err != nil {
		t.Fatalf("GetSyncState() error: %v", err)
	}
	if state != "cursor-123" {
		t.Fatalf("expected %q, got %q", "cursor-123", state)
	}

	// Update state.
	if err := c.SetSyncState(ctx, "markdown", "cursor-456"); err != nil {
		t.Fatalf("SetSyncState() update error: %v", err)
	}

	state, err = c.GetSyncState(ctx, "markdown")
	if err != nil {
		t.Fatalf("GetSyncState() error: %v", err)
	}
	if state != "cursor-456" {
		t.Fatalf("expected %q, got %q", "cursor-456", state)
	}
}
