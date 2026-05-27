package main

import "testing"

func TestParseLintArgs_Defaults(t *testing.T) {
	opts, err := parseLintArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts.LowConfidence {
		t.Error("LowConfidence should default to false")
	}
	if opts.LowConfidenceThreshold != 0 {
		t.Errorf("LowConfidenceThreshold default = %v, want 0 (cortex applies internal default)",
			opts.LowConfidenceThreshold)
	}
	if opts.OutPath != "" {
		t.Errorf("OutPath default = %q, want empty", opts.OutPath)
	}
}

func TestParseLintArgs_LowConfidence(t *testing.T) {
	opts, err := parseLintArgs([]string{"--low-confidence"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.LowConfidence {
		t.Error("LowConfidence should be true")
	}
}

func TestParseLintArgs_ThresholdImpliesEnable(t *testing.T) {
	opts, err := parseLintArgs([]string{"--low-confidence-threshold", "0.5"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.LowConfidence {
		t.Error("--low-confidence-threshold should imply --low-confidence")
	}
	if opts.LowConfidenceThreshold != 0.5 {
		t.Errorf("LowConfidenceThreshold = %v, want 0.5", opts.LowConfidenceThreshold)
	}
}

func TestParseLintArgs_OutPath(t *testing.T) {
	opts, err := parseLintArgs([]string{"--out", "/tmp/lint.md"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.OutPath != "/tmp/lint.md" {
		t.Errorf("OutPath = %q, want /tmp/lint.md", opts.OutPath)
	}
}

func TestParseLintArgs_UnknownFlag(t *testing.T) {
	_, err := parseLintArgs([]string{"--bogus"})
	if err == nil {
		t.Error("expected error for unknown flag")
	}
}

func TestParseLintArgs_ThresholdRequiresValue(t *testing.T) {
	_, err := parseLintArgs([]string{"--low-confidence-threshold"})
	if err == nil {
		t.Error("expected error for missing value")
	}
}

func TestParseLintArgs_InvalidThreshold(t *testing.T) {
	_, err := parseLintArgs([]string{"--low-confidence-threshold", "notanumber"})
	if err == nil {
		t.Error("expected error for non-numeric threshold")
	}
}
