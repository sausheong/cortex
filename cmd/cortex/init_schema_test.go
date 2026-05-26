package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitSchema_WritesTemplate(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "CORTEX.md")
	if err := writeSchemaTo(target, false); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "# CORTEX.md") {
		t.Error("expected written file to contain '# CORTEX.md' heading")
	}
}

func TestInitSchema_RefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "CORTEX.md")
	if err := os.WriteFile(target, []byte("pre-existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := writeSchemaTo(target, false)
	if err == nil {
		t.Fatal("expected error when target exists without --force")
	}
	data, _ := os.ReadFile(target)
	if string(data) != "pre-existing" {
		t.Errorf("file was overwritten without --force: %q", string(data))
	}
}

func TestInitSchema_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "CORTEX.md")
	if err := os.WriteFile(target, []byte("pre-existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeSchemaTo(target, true); err != nil {
		t.Fatalf("force should succeed: %v", err)
	}
	data, _ := os.ReadFile(target)
	if string(data) == "pre-existing" {
		t.Error("file should have been overwritten with --force")
	}
}
