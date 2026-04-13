package markdown

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/extractor/deterministic"
)

func TestSyncMarkdownFiles(t *testing.T) {
	// Create temp directory with markdown files.
	dir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	md1 := `---
name: Alice
type: person
role: engineer
---

Alice works at Acme Corp and knows [[Bob]].
`
	md2 := `---
name: Bob
type: person
role: manager
---

Bob manages the engineering team.
`

	if err := os.WriteFile(filepath.Join(dir, "alice.md"), []byte(md1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bob.md"), []byte(md2), 0o644); err != nil {
		t.Fatal(err)
	}

	// Open Cortex with deterministic extractor.
	cx, err := cortex.Open(dbPath, cortex.WithExtractor(deterministic.New()))
	if err != nil {
		t.Fatal(err)
	}
	defer cx.Close()

	// Sync.
	conn := New(dir)
	ctx := context.Background()
	if err := conn.Sync(ctx, cx); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	// Verify entities were created.
	entities, err := cx.FindEntities(ctx, cortex.EntityFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entities) == 0 {
		t.Fatal("expected entities to be created, got 0")
	}

	// Check that we have entities from frontmatter parsing.
	var foundAlice, foundBob bool
	for _, e := range entities {
		if e.Name == "Alice" {
			foundAlice = true
		}
		if e.Name == "Bob" {
			foundBob = true
		}
	}
	if !foundAlice {
		t.Error("expected Alice entity from frontmatter")
	}
	if !foundBob {
		t.Error("expected Bob entity from frontmatter or wikilink")
	}
}

func TestSyncIsIncremental(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	md1 := `---
name: Charlie
type: person
---

Charlie is a developer.
`
	filePath := filepath.Join(dir, "charlie.md")
	if err := os.WriteFile(filePath, []byte(md1), 0o644); err != nil {
		t.Fatal(err)
	}

	cx, err := cortex.Open(dbPath, cortex.WithExtractor(deterministic.New()))
	if err != nil {
		t.Fatal(err)
	}
	defer cx.Close()

	conn := New(dir)
	ctx := context.Background()

	// First sync.
	if err := conn.Sync(ctx, cx); err != nil {
		t.Fatalf("first sync failed: %v", err)
	}

	entities1, err := cx.FindEntities(ctx, cortex.EntityFilter{})
	if err != nil {
		t.Fatal(err)
	}
	count1 := len(entities1)

	// Wait a moment so the sync timestamp is clearly after the file mod time.
	time.Sleep(50 * time.Millisecond)

	// Second sync without changes — should not create duplicates.
	if err := conn.Sync(ctx, cx); err != nil {
		t.Fatalf("second sync failed: %v", err)
	}

	entities2, err := cx.FindEntities(ctx, cortex.EntityFilter{})
	if err != nil {
		t.Fatal(err)
	}
	count2 := len(entities2)

	// Entity count should be the same since PutEntity upserts by name+type.
	if count2 != count1 {
		t.Errorf("expected %d entities after second sync, got %d (duplicates created)", count1, count2)
	}
}
