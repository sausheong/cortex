package cortex

import (
	"strings"
	"testing"
	"time"
)

func TestRenderReconcileMarkdown(t *testing.T) {
	rep := ReconcileReport{
		EntitiesScanned: 1,
		MemoriesScanned: 2,
		Proposed: []Supersession{{
			StaleID: "a", StaleContent: "budget 5000",
			SupersededByID: "b", SupersededByContent: "budget 10000",
			Reason: "changed", InvalidAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		}},
	}
	md := RenderReconcileMarkdown(rep)
	for _, want := range []string{"budget 5000", "budget 10000", "changed", "1"} {
		if !strings.Contains(md, want) {
			t.Fatalf("rendered report missing %q:\n%s", want, md)
		}
	}
}

func TestRenderReconcileMarkdownMode_Applied(t *testing.T) {
	rep := ReconcileReport{
		EntitiesScanned: 1,
		MemoriesScanned: 2,
		Proposed: []Supersession{{
			StaleID: "a", StaleContent: "budget 5000",
			SupersededByID: "b", SupersededByContent: "budget 10000",
			Reason: "changed", InvalidAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		}},
		Rejected: []RejectedPair{{
			StaleID: "c", SupersededByID: "d", Reason: "no longer valid",
		}},
	}
	md := RenderReconcileMarkdownMode(rep, true)
	for _, want := range []string{"Applied", "Skipped"} {
		if !strings.Contains(md, want) {
			t.Fatalf("applied report missing %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "Proposed Supersessions") {
		t.Fatalf("applied report should not contain dry-run header %q:\n%s", "Proposed Supersessions", md)
	}
}

func TestRenderReconcileMarkdown_Skipped(t *testing.T) {
	md := RenderReconcileMarkdown(ReconcileReport{Skipped: true, SkipReason: "no reconciler"})
	if !strings.Contains(md, "no reconciler") {
		t.Fatalf("expected skip reason in output, got:\n%s", md)
	}
}
