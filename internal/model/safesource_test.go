package model

import "testing"

func TestWithSourceRenamesReservedCorrection(t *testing.T) {
	for _, in := range []string{SourceVaultCorrection, "  Vault Correction "} {
		var c RememberConfig
		WithSource(in)(&c)
		if c.Source == SourceVaultCorrection || c.Source == "" {
			t.Fatalf("reserved source passed through for %q: %q", in, c.Source)
		}
	}
	var c RememberConfig
	WithSource("notes")(&c)
	if c.Source != "notes" {
		t.Fatalf("got %q", c.Source)
	}
}
