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
