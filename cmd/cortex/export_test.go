package main

import "testing"

func TestParseExportArgs_Defaults(t *testing.T) {
	opts, err := parseExportArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts.VaultDir != "./vault" {
		t.Errorf("VaultDir = %q, want ./vault", opts.VaultDir)
	}
	if opts.Full {
		t.Error("Full should default to false")
	}
	if opts.DryRun {
		t.Error("DryRun should default to false")
	}
}

func TestParseExportArgs_AllFlags(t *testing.T) {
	opts, err := parseExportArgs([]string{"--vault", "/tmp/v", "--full", "--dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.VaultDir != "/tmp/v" {
		t.Errorf("VaultDir = %q", opts.VaultDir)
	}
	if !opts.Full {
		t.Error("Full should be true")
	}
	if !opts.DryRun {
		t.Error("DryRun should be true")
	}
}

func TestParseExportArgs_UnknownFlagErrors(t *testing.T) {
	_, err := parseExportArgs([]string{"--unknown"})
	if err == nil {
		t.Error("expected error for unknown flag")
	}
}
