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

func TestParseRememberArgs_Defaults(t *testing.T) {
	text, speaker, source, err := parseRememberArgs([]string{"alice", "likes", "coffee"})
	if err != nil {
		t.Fatal(err)
	}
	if text != "alice likes coffee" {
		t.Errorf("text = %q, want %q", text, "alice likes coffee")
	}
	if speaker != "" {
		t.Errorf("speaker = %q, want empty", speaker)
	}
	if source != "" {
		t.Errorf("source = %q, want empty", source)
	}
}

func TestParseRememberArgs_Speaker(t *testing.T) {
	text, speaker, source, err := parseRememberArgs([]string{"likes", "coffee", "--speaker", "user"})
	if err != nil {
		t.Fatal(err)
	}
	if text != "likes coffee" {
		t.Errorf("text = %q, want %q", text, "likes coffee")
	}
	if speaker != "user" {
		t.Errorf("speaker = %q, want user", speaker)
	}
	if source != "" {
		t.Errorf("source = %q, want empty", source)
	}
}

func TestParseRememberArgs_Source(t *testing.T) {
	text, speaker, source, err := parseRememberArgs([]string{"--source", "notes.md", "the", "fact"})
	if err != nil {
		t.Fatal(err)
	}
	if text != "the fact" {
		t.Errorf("text = %q, want %q", text, "the fact")
	}
	if speaker != "" {
		t.Errorf("speaker = %q, want empty", speaker)
	}
	if source != "notes.md" {
		t.Errorf("source = %q, want notes.md", source)
	}
}

func TestParseRememberArgs_SpeakerAndSource(t *testing.T) {
	text, speaker, source, err := parseRememberArgs([]string{"a", "fact", "--speaker", "assistant", "--source", "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if text != "a fact" {
		t.Errorf("text = %q, want %q", text, "a fact")
	}
	if speaker != "assistant" {
		t.Errorf("speaker = %q, want assistant", speaker)
	}
	if source != "chat" {
		t.Errorf("source = %q, want chat", source)
	}
}

func TestParseRememberArgs_MissingFlagValue(t *testing.T) {
	_, _, _, err := parseRememberArgs([]string{"a", "fact", "--speaker"})
	if err == nil {
		t.Error("expected error for --speaker with no value")
	}
	_, _, _, err = parseRememberArgs([]string{"a", "fact", "--source"})
	if err == nil {
		t.Error("expected error for --source with no value")
	}
}

func TestParseRememberArgs_EmptyText(t *testing.T) {
	_, _, _, err := parseRememberArgs([]string{})
	if err == nil {
		t.Error("expected error for no text")
	}
	_, _, _, err = parseRememberArgs([]string{"--speaker", "user"})
	if err == nil {
		t.Error("expected error for flags-only with no text")
	}
}

func TestParseRememberArgs_UnknownBecomesText(t *testing.T) {
	// Non-flag args (and flags other than --speaker/--source) join into text,
	// matching the prior behavior of joining all args as the fact text.
	text, speaker, source, err := parseRememberArgs([]string{"some", "--weird", "thing"})
	if err != nil {
		t.Fatal(err)
	}
	if text != "some --weird thing" {
		t.Errorf("text = %q, want %q", text, "some --weird thing")
	}
	if speaker != "" || source != "" {
		t.Errorf("speaker/source should be empty, got %q/%q", speaker, source)
	}
}
