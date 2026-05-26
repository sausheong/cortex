package vault

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestManifest_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".cortex-export.json")

	m := newManifest()
	m.Pages["ent_01"] = PageEntry{
		Path:        "people/alice.md",
		ContentHash: "sha256:abc",
		ExportedAt:  time.Date(2026, 5, 26, 14, 32, 10, 0, time.UTC),
	}
	if err := saveManifest(path, m); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := loadManifest(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Version != manifestVersion {
		t.Errorf("version = %d, want %d", loaded.Version, manifestVersion)
	}
	if got := loaded.Pages["ent_01"].Path; got != "people/alice.md" {
		t.Errorf("path = %q, want people/alice.md", got)
	}
	if got := loaded.Pages["ent_01"].ContentHash; got != "sha256:abc" {
		t.Errorf("hash = %q, want sha256:abc", got)
	}
}

func TestManifest_MissingFileReturnsEmpty(t *testing.T) {
	m, err := loadManifest(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file should be empty, not error: %v", err)
	}
	if m.Version != manifestVersion {
		t.Errorf("version = %d, want %d", m.Version, manifestVersion)
	}
	if len(m.Pages) != 0 {
		t.Errorf("expected empty pages map, got %d entries", len(m.Pages))
	}
}

func TestManifest_CorruptFileReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".cortex-export.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadManifest(path)
	if err == nil {
		t.Fatal("expected error for corrupt manifest, got nil")
	}
}
