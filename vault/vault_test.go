package vault

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sausheong/cortex"
)

// seedBrain creates a temp brain.db with a small fixed graph and returns
// an open *cortex.Cortex plus a cleanup func. No LLM / embedder configured.
func seedBrain(t *testing.T) (*cortex.Cortex, func()) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "brain.db")
	cx, err := cortex.Open(dbPath)
	if err != nil {
		t.Fatalf("open cortex: %v", err)
	}
	ctx := context.Background()
	alice := &cortex.Entity{Type: "person", Name: "Alice Chen", Source: "notes/intro.md"}
	stripe := &cortex.Entity{Type: "organization", Name: "Stripe"}
	if err := cx.PutEntity(ctx, alice); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutEntity(ctx, stripe); err != nil {
		t.Fatal(err)
	}
	rel := &cortex.Relationship{SourceID: alice.ID, TargetID: stripe.ID, Type: "works_at"}
	if err := cx.PutRelationship(ctx, rel); err != nil {
		t.Fatal(err)
	}
	mem := &cortex.Memory{Content: "Alice joined Stripe", EntityIDs: []string{alice.ID, stripe.ID}, Source: "notes/intro.md"}
	if err := cx.PutMemory(ctx, mem); err != nil {
		t.Fatal(err)
	}
	return cx, func() { _ = cx.Close() }
}

func TestExport_BasicTree(t *testing.T) {
	cx, cleanup := seedBrain(t)
	defer cleanup()
	vaultDir := t.TempDir()

	stats, err := Export(context.Background(), cx, Options{VaultDir: vaultDir})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if stats.Written < 2 {
		t.Errorf("Written = %d, want >= 2 (alice + stripe)", stats.Written)
	}

	mustExist(t, vaultDir, "people/alice-chen.md")
	mustExist(t, vaultDir, "organizations/stripe.md")
	mustExist(t, vaultDir, "index.md")
	mustExist(t, vaultDir, "log.md")
	mustExist(t, vaultDir, ManifestFilename)

	entries, _ := os.ReadDir(filepath.Join(vaultDir, "sources"))
	if len(entries) == 0 {
		t.Error("expected at least one source page")
	}
}

func TestExport_Idempotent(t *testing.T) {
	cx, cleanup := seedBrain(t)
	defer cleanup()
	vaultDir := t.TempDir()
	ctx := context.Background()

	first, err := Export(ctx, cx, Options{VaultDir: vaultDir})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Export(ctx, cx, Options{VaultDir: vaultDir})
	if err != nil {
		t.Fatal(err)
	}
	if second.Written != 0 {
		t.Errorf("second run Written = %d, want 0", second.Written)
	}
	if second.Skipped < first.Written {
		t.Errorf("second run Skipped = %d, want >= %d", second.Skipped, first.Written)
	}
}

func TestExport_DeletedEntityIsArchived(t *testing.T) {
	cx, cleanup := seedBrain(t)
	defer cleanup()
	vaultDir := t.TempDir()
	ctx := context.Background()

	if _, err := Export(ctx, cx, Options{VaultDir: vaultDir}); err != nil {
		t.Fatal(err)
	}
	if err := cx.Forget(ctx, cortex.Filter{Source: "notes/intro.md"}); err != nil {
		t.Fatal(err)
	}
	stats, err := Export(ctx, cx, Options{VaultDir: vaultDir})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Archived == 0 {
		t.Error("expected at least one archived page")
	}
	if _, err := os.Stat(filepath.Join(vaultDir, "people/alice-chen.md")); !os.IsNotExist(err) {
		t.Error("alice page should have been moved to _archive")
	}
	entries, _ := os.ReadDir(filepath.Join(vaultDir, "_archive"))
	if len(entries) == 0 {
		t.Error("_archive should have at least one timestamped subdir")
	}
}

func TestExport_Full_RewritesEverything(t *testing.T) {
	cx, cleanup := seedBrain(t)
	defer cleanup()
	vaultDir := t.TempDir()
	ctx := context.Background()

	first, err := Export(ctx, cx, Options{VaultDir: vaultDir})
	if err != nil {
		t.Fatal(err)
	}
	full, err := Export(ctx, cx, Options{VaultDir: vaultDir, Full: true})
	if err != nil {
		t.Fatal(err)
	}
	if full.Written != first.Written {
		t.Errorf("full run Written = %d, want %d (same as first)", full.Written, first.Written)
	}
	if full.Skipped != 0 {
		t.Errorf("full run Skipped = %d, want 0", full.Skipped)
	}
}

func TestExport_DryRun_NoFilesWritten(t *testing.T) {
	cx, cleanup := seedBrain(t)
	defer cleanup()
	vaultDir := t.TempDir()

	stats, err := Export(context.Background(), cx, Options{VaultDir: vaultDir, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Written == 0 {
		t.Error("DryRun should still report Written count")
	}
	if _, err := os.Stat(filepath.Join(vaultDir, ManifestFilename)); !os.IsNotExist(err) {
		t.Error("DryRun must not write manifest")
	}
	entries, _ := os.ReadDir(filepath.Join(vaultDir, "people"))
	if len(entries) > 0 {
		t.Error("DryRun must not write entity pages")
	}
}

func TestExport_LogAppends(t *testing.T) {
	cx, cleanup := seedBrain(t)
	defer cleanup()
	vaultDir := t.TempDir()
	ctx := context.Background()

	if _, err := Export(ctx, cx, Options{VaultDir: vaultDir}); err != nil {
		t.Fatal(err)
	}
	if _, err := Export(ctx, cx, Options{VaultDir: vaultDir}); err != nil {
		t.Fatal(err)
	}
	logBytes, err := os.ReadFile(filepath.Join(vaultDir, "log.md"))
	if err != nil {
		t.Fatal(err)
	}
	count := strings.Count(string(logBytes), "## [")
	if count != 2 {
		t.Errorf("expected 2 log entries after 2 runs, got %d", count)
	}
}

func mustExist(t *testing.T, root, rel string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
		t.Errorf("expected %s to exist: %v", rel, err)
	}
}

func TestExport_EmptyGraphArchivesPreviousPages(t *testing.T) {
	cx, cleanup := seedBrain(t)
	defer cleanup()
	vaultDir := t.TempDir()
	ctx := context.Background()

	// Initial export with content.
	first, err := Export(ctx, cx, Options{VaultDir: vaultDir})
	if err != nil {
		t.Fatal(err)
	}
	if first.Written < 2 {
		t.Fatalf("setup: expected >=2 entities written, got %d", first.Written)
	}

	// Wipe the graph entirely.
	if err := cx.Forget(ctx, cortex.Filter{Source: "notes/intro.md"}); err != nil {
		t.Fatal(err)
	}
	// Also forget Stripe by entity ID — Filter{Source: "notes/intro.md"} won't
	// catch Stripe because it has no source. Query for all entities and delete.
	all, _ := cx.FindEntities(ctx, cortex.EntityFilter{})
	for _, e := range all {
		if err := cx.Forget(ctx, cortex.Filter{EntityID: e.ID}); err != nil {
			t.Fatal(err)
		}
	}

	// Second export — graph is empty.
	stats, err := Export(ctx, cx, Options{VaultDir: vaultDir})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Archived == 0 {
		t.Error("expected archive count > 0 after emptying graph")
	}
	// alice and stripe pages must be gone from their type folders.
	if _, err := os.Stat(filepath.Join(vaultDir, "people/alice-chen.md")); !os.IsNotExist(err) {
		t.Error("alice page should be archived")
	}
	if _, err := os.Stat(filepath.Join(vaultDir, "organizations/stripe.md")); !os.IsNotExist(err) {
		t.Error("stripe page should be archived")
	}
}

func TestExport_EntityRenameMovesPage(t *testing.T) {
	// NOTE: cortex.PutEntity upserts by (name, type), not by ID — calling
	// PutEntity with a different Name inserts a brand-new row rather than
	// updating the existing one. So a true "rename" requires delete-then-add:
	// the old entity disappears from the graph (its page archives), and a
	// new entity with a different slug appears (its page is written).
	cx, cleanup := seedBrain(t)
	defer cleanup()
	vaultDir := t.TempDir()
	ctx := context.Background()

	if _, err := Export(ctx, cx, Options{VaultDir: vaultDir}); err != nil {
		t.Fatal(err)
	}
	// Find alice, delete her, then add "Alice Lin" (different slug).
	people, _ := cx.FindEntities(ctx, cortex.EntityFilter{Type: "person"})
	if len(people) == 0 {
		t.Fatal("expected at least one person")
	}
	alice := people[0]
	if err := cx.Forget(ctx, cortex.Filter{EntityID: alice.ID}); err != nil {
		t.Fatal(err)
	}
	renamed := &cortex.Entity{Type: "person", Name: "Alice Lin", Source: alice.Source}
	if err := cx.PutEntity(ctx, renamed); err != nil {
		t.Fatal(err)
	}
	if _, err := Export(ctx, cx, Options{VaultDir: vaultDir}); err != nil {
		t.Fatal(err)
	}
	// Old page should be gone, new page should exist.
	if _, err := os.Stat(filepath.Join(vaultDir, "people/alice-chen.md")); !os.IsNotExist(err) {
		t.Error("old alice-chen.md should be removed after rename")
	}
	if _, err := os.Stat(filepath.Join(vaultDir, "people/alice-lin.md")); err != nil {
		t.Errorf("new alice-lin.md should exist: %v", err)
	}
}

func TestExport_CollidingSourcesProduceMatchingLinks(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "brain.db")
	cx, err := cortex.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer cx.Close()
	ctx := context.Background()

	// Two entities, each from a different source, but the two sources
	// slug to the same base ("notes-intro").
	a := &cortex.Entity{Type: "person", Name: "AlphaUser", Source: "notes/intro.md"}
	b := &cortex.Entity{Type: "person", Name: "BetaUser", Source: "notes-intro.md"}
	if err := cx.PutEntity(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutEntity(ctx, b); err != nil {
		t.Fatal(err)
	}

	vaultDir := t.TempDir()
	if _, err := Export(ctx, cx, Options{VaultDir: vaultDir}); err != nil {
		t.Fatal(err)
	}

	// List the source filenames actually on disk.
	entries, err := os.ReadDir(filepath.Join(vaultDir, "sources"))
	if err != nil {
		t.Fatal(err)
	}
	onDisk := map[string]bool{}
	for _, e := range entries {
		onDisk[strings.TrimSuffix(e.Name(), ".md")] = true
	}
	if len(onDisk) < 2 {
		t.Fatalf("expected 2+ distinct source files, got %d", len(onDisk))
	}

	// For each entity page, parse out the [[sources/X]] wikilinks and verify
	// every X is an actual file on disk.
	for _, ent := range []*cortex.Entity{a, b} {
		page := filepath.Join(vaultDir, "people", slug(ent.Name)+".md")
		body, err := os.ReadFile(page)
		if err != nil {
			t.Fatalf("read %s: %v", page, err)
		}
		// Naive but sufficient: scan for "[[sources/" and pull until "]]".
		s := string(body)
		idx := 0
		found := 0
		for {
			i := strings.Index(s[idx:], "[[sources/")
			if i < 0 {
				break
			}
			start := idx + i + len("[[sources/")
			end := strings.Index(s[start:], "]]")
			if end < 0 {
				t.Fatalf("malformed wikilink in %s", page)
			}
			link := s[start : start+end]
			// Strip any |alias suffix.
			if pipe := strings.Index(link, "|"); pipe >= 0 {
				link = link[:pipe]
			}
			if !onDisk[link] {
				t.Errorf("%s links to [[sources/%s]] but no such file in vault/sources/", ent.Name, link)
			}
			found++
			idx = start + end + 2
		}
		if found == 0 {
			t.Errorf("%s page has no source wikilinks", ent.Name)
		}
	}
}

func TestExport_SourceSlugCollisionHandled(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "brain.db")
	cx, err := cortex.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer cx.Close()
	ctx := context.Background()

	// Two entities with different sources that slug to the same base.
	a := &cortex.Entity{Type: "person", Name: "A", Source: "notes/intro.md"}
	b := &cortex.Entity{Type: "person", Name: "B", Source: "notes-intro.md"}
	if err := cx.PutEntity(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutEntity(ctx, b); err != nil {
		t.Fatal(err)
	}

	vaultDir := t.TempDir()
	if _, err := Export(ctx, cx, Options{VaultDir: vaultDir}); err != nil {
		t.Fatal(err)
	}

	// vault/sources should have TWO distinct files, not one overwriting the other.
	entries, err := os.ReadDir(filepath.Join(vaultDir, "sources"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 {
		t.Errorf("expected >= 2 source files for distinct sources that slug-collide, got %d", len(entries))
		for _, e := range entries {
			t.Logf("  found: %s", e.Name())
		}
	}
}
