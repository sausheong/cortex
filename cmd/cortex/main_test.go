package main

import (
	"testing"
)

func TestParseMergeArgs_Defaults(t *testing.T) {
	opts, err := parseMergeArgs([]string{"ent_keep", "ent_drop"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.KeepID != "ent_keep" {
		t.Errorf("KeepID = %q, want ent_keep", opts.KeepID)
	}
	if opts.DropID != "ent_drop" {
		t.Errorf("DropID = %q, want ent_drop", opts.DropID)
	}
	if opts.DryRun {
		t.Error("DryRun should default to false")
	}
}

func TestParseMergeArgs_DryRun(t *testing.T) {
	opts, err := parseMergeArgs([]string{"ent_keep", "ent_drop", "--dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.DryRun {
		t.Error("DryRun should be true")
	}
	opts2, err := parseMergeArgs([]string{"--dry-run", "ent_keep", "ent_drop"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts2.DryRun || opts2.KeepID != "ent_keep" || opts2.DropID != "ent_drop" {
		t.Errorf("flag-first parse wrong: %+v", opts2)
	}
}

func TestParseMergeArgs_MissingIDs(t *testing.T) {
	_, err := parseMergeArgs([]string{"ent_keep"})
	if err == nil {
		t.Error("expected error for single positional arg")
	}
	_, err = parseMergeArgs([]string{})
	if err == nil {
		t.Error("expected error for no args")
	}
}

func TestParseMergeArgs_UnknownFlag(t *testing.T) {
	_, err := parseMergeArgs([]string{"ent_keep", "ent_drop", "--unknown"})
	if err == nil {
		t.Error("expected error for unknown flag")
	}
}
