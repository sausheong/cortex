package files

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

	cx, err := cortex.Open(dbPath, cortex.WithExtractor(deterministic.New()))
	if err != nil {
		t.Fatal(err)
	}
	defer cx.Close()

	conn := New(dir)
	ctx := context.Background()
	if err := conn.Sync(ctx, cx); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	entities, err := cx.FindEntities(ctx, cortex.EntityFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entities) == 0 {
		t.Fatal("expected entities to be created, got 0")
	}

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

func TestSyncCSVFiles(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	csv := `name,role,company
Alice,Engineer,Stripe
Bob,Manager,Google
`
	if err := os.WriteFile(filepath.Join(dir, "team.csv"), []byte(csv), 0o644); err != nil {
		t.Fatal(err)
	}

	cx, err := cortex.Open(dbPath, cortex.WithExtractor(deterministic.New()))
	if err != nil {
		t.Fatal(err)
	}
	defer cx.Close()

	conn := New(dir)
	ctx := context.Background()
	if err := conn.Sync(ctx, cx); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	// CSV content should be stored as a chunk even without LLM extraction.
	results, err := cx.SearchKeyword(ctx, "Alice", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Error("expected CSV content to be searchable via keyword")
	}
}

func TestSyncYAMLFiles(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	yaml := `people:
  - name: Carol
    role: CTO
    company: Acme
  - name: Dave
    role: VP Engineering
    company: Acme
`
	if err := os.WriteFile(filepath.Join(dir, "org.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cx, err := cortex.Open(dbPath, cortex.WithExtractor(deterministic.New()))
	if err != nil {
		t.Fatal(err)
	}
	defer cx.Close()

	conn := New(dir)
	ctx := context.Background()
	if err := conn.Sync(ctx, cx); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	results, err := cx.SearchKeyword(ctx, "Carol", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Error("expected YAML content to be searchable via keyword")
	}
}

func TestSyncJSONFiles(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	jsonContent := `{"employees": [{"name": "Eve", "role": "Designer"}, {"name": "Frank", "role": "Developer"}]}`
	if err := os.WriteFile(filepath.Join(dir, "staff.json"), []byte(jsonContent), 0o644); err != nil {
		t.Fatal(err)
	}

	cx, err := cortex.Open(dbPath, cortex.WithExtractor(deterministic.New()))
	if err != nil {
		t.Fatal(err)
	}
	defer cx.Close()

	conn := New(dir)
	ctx := context.Background()
	if err := conn.Sync(ctx, cx); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	results, err := cx.SearchKeyword(ctx, "Eve", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Error("expected JSON content to be searchable via keyword")
	}
}

func TestSyncSkipsUnsupportedFiles(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	// Write a binary-like file with unsupported extension.
	if err := os.WriteFile(filepath.Join(dir, "image.png"), []byte("fake png data"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Write a supported file.
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("this is a note"), 0o644); err != nil {
		t.Fatal(err)
	}

	cx, err := cortex.Open(dbPath, cortex.WithExtractor(deterministic.New()))
	if err != nil {
		t.Fatal(err)
	}
	defer cx.Close()

	conn := New(dir)
	ctx := context.Background()
	if err := conn.Sync(ctx, cx); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	// The .txt file should be searchable.
	results, err := cx.SearchKeyword(ctx, "note", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Error("expected .txt content to be searchable")
	}

	// The .png file should not be ingested.
	results, err = cx.SearchKeyword(ctx, "fake png", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Error("expected .png file to be skipped")
	}
}

func TestSyncIsIncremental(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	md := `---
name: Charlie
type: person
---

Charlie is a developer.
`
	if err := os.WriteFile(filepath.Join(dir, "charlie.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}

	cx, err := cortex.Open(dbPath, cortex.WithExtractor(deterministic.New()))
	if err != nil {
		t.Fatal(err)
	}
	defer cx.Close()

	conn := New(dir)
	ctx := context.Background()

	if err := conn.Sync(ctx, cx); err != nil {
		t.Fatalf("first sync failed: %v", err)
	}

	entities1, err := cx.FindEntities(ctx, cortex.EntityFilter{})
	if err != nil {
		t.Fatal(err)
	}
	count1 := len(entities1)

	time.Sleep(50 * time.Millisecond)

	if err := conn.Sync(ctx, cx); err != nil {
		t.Fatalf("second sync failed: %v", err)
	}

	entities2, err := cx.FindEntities(ctx, cortex.EntityFilter{})
	if err != nil {
		t.Fatal(err)
	}
	count2 := len(entities2)

	if count2 != count1 {
		t.Errorf("expected %d entities after second sync, got %d (duplicates created)", count1, count2)
	}
}

func TestSyncMixedFileTypes(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	md := `---
name: Grace
type: person
---
Grace is a researcher.
`
	csv := "project,lead\nAlpha,Grace\nBeta,Hank\n"
	yaml := "team:\n  - Grace\n  - Hank\n"

	if err := os.WriteFile(filepath.Join(dir, "grace.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "projects.csv"), []byte(csv), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "team.yml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cx, err := cortex.Open(dbPath, cortex.WithExtractor(deterministic.New()))
	if err != nil {
		t.Fatal(err)
	}
	defer cx.Close()

	conn := New(dir)
	ctx := context.Background()
	if err := conn.Sync(ctx, cx); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	// Grace should appear from multiple sources.
	results, err := cx.SearchKeyword(ctx, "Grace", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) < 2 {
		t.Errorf("expected Grace in multiple files, got %d results", len(results))
	}
}

func TestContentType(t *testing.T) {
	tests := []struct {
		ext  string
		want string
	}{
		{".md", "markdown"},
		{".csv", "csv"},
		{".yaml", "yaml"},
		{".yml", "yaml"},
		{".json", "json"},
		{".txt", "text"},
		{".tsv", "tsv"},
		{".xml", "xml"},
		{".toml", "toml"},
		{".log", "text"},
		{".png", ""},
		{".exe", ""},
	}

	for _, tt := range tests {
		got := ContentType(tt.ext)
		if got != tt.want {
			t.Errorf("ContentType(%q) = %q, want %q", tt.ext, got, tt.want)
		}
	}
}

func TestSupported(t *testing.T) {
	if !Supported(".md") {
		t.Error("expected .md to be supported")
	}
	if !Supported(".CSV") {
		t.Error("expected .CSV (uppercase) to be supported")
	}
	if Supported(".png") {
		t.Error("expected .png to not be supported")
	}
}
