package cortex

import (
	"strings"
	"testing"
)

func TestRenderMaintainMarkdown(t *testing.T) {
	r := MaintainReport{
		DryRun:    true,
		Reconcile: &ReconcileReport{EntitiesScanned: 1, MemoriesScanned: 2},
		Relate:    &RelationReport{Skipped: true, SkipReason: "relate has no dry-run; skipped under --dry-run"},
		Decay:     &DecayReport{Scanned: 3, Decayed: 1, DryRun: true},
	}
	out := RenderMaintainMarkdown(r)
	for _, want := range []string{"Maintain", "Reconcile", "Relation", "Decay", "dry-run", "skipped under"} {
		if !strings.Contains(strings.ToLower(out), strings.ToLower(want)) {
			t.Fatalf("expected output to contain %q, got:\n%s", want, out)
		}
	}

	// Nil sub-report (toggled off) → a skipped note, no panic.
	r2 := MaintainReport{Decay: &DecayReport{Scanned: 0}}
	out2 := RenderMaintainMarkdown(r2)
	if !strings.Contains(out2, "Decay") {
		t.Fatalf("expected decay section, got:\n%s", out2)
	}
}

func TestRenderMaintainMarkdown_IncludesProfile(t *testing.T) {
	r := MaintainReport{
		Profile: &ProfileReport{Scanned: 2, Rebuilt: 2},
	}
	md := RenderMaintainMarkdown(r)
	if !strings.Contains(md, "## Profile") {
		t.Errorf("expected Profile section in:\n%s", md)
	}
	if !strings.Contains(md, "Rebuilt: 2") {
		t.Errorf("expected profile detail in:\n%s", md)
	}
}

func TestRenderMaintainMarkdown_ProfileSkipped(t *testing.T) {
	md := RenderMaintainMarkdown(MaintainReport{})
	if !strings.Contains(md, "## Profile") || !strings.Contains(md, "_skipped_") {
		t.Errorf("expected skipped Profile section in:\n%s", md)
	}
}
