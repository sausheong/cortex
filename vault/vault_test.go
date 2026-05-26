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
