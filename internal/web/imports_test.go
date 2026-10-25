package web

import (
	"os/exec"
	"strings"
	"testing"
)

// The web app serves Vault articles; the per-entity markdown projection in
// the vault package is CLI-only and must not creep back into it.
func TestWebDoesNotImportVaultProjection(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	for _, p := range strings.Fields(string(out)) {
		if p == "github.com/sausheong/cortex/vault" {
			t.Fatalf("internal/web depends on %s", p)
		}
	}
}
