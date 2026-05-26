package cortex

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
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

func TestEnsureColumn_AddsMissingColumn(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE t (id TEXT PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatal(err)
	}

	if err := ensureColumn(db, "t", "confidence", "REAL NOT NULL DEFAULT 1.0"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	// Idempotent — second call is a no-op.
	if err := ensureColumn(db, "t", "confidence", "REAL NOT NULL DEFAULT 1.0"); err != nil {
		t.Fatalf("second call should be no-op: %v", err)
	}

	// Verify column exists and has the right default.
	if _, err := db.Exec(`INSERT INTO t (id, name) VALUES ('x', 'hello')`); err != nil {
		t.Fatal(err)
	}
	var c float64
	if err := db.QueryRow(`SELECT confidence FROM t WHERE id = 'x'`).Scan(&c); err != nil {
		t.Fatal(err)
	}
	if c != 1.0 {
		t.Errorf("default confidence = %v, want 1.0", c)
	}
}

func TestOpen_AddsConfidenceColumnsToLegacyDB(t *testing.T) {
	t.Skip("requires Task 2 (Entity.Confidence field)")

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "legacy.db")

	// Build a "legacy" db with only the pre-confidence schema.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	legacyDDL := `
		CREATE TABLE entities (id TEXT PRIMARY KEY, type TEXT NOT NULL, name TEXT NOT NULL, attributes TEXT, source TEXT, created_at DATETIME, updated_at DATETIME);
		CREATE TABLE relationships (id TEXT PRIMARY KEY, source_id TEXT, target_id TEXT, type TEXT NOT NULL, attributes TEXT, source TEXT, created_at DATETIME);
		CREATE TABLE memories (id TEXT PRIMARY KEY, content TEXT NOT NULL, source TEXT, created_at DATETIME, updated_at DATETIME);
	`
	if _, err := db.Exec(legacyDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO entities (id, type, name) VALUES ('legacy1', 'person', 'Old Alice')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Open with the new code — should add columns without errors.
	cx, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer cx.Close()

	// Legacy row should now have confidence=1.0.
	e, err := cx.GetEntity(context.Background(), "legacy1")
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	_ = e
	// TODO(task-2): un-skip and uncomment when Entity.Confidence is added.
	// if e.Confidence != 1.0 {
	// 	t.Errorf("legacy row confidence = %v, want 1.0", e.Confidence)
	// }
}
