package cortex

import (
	"strings"
	"testing"
)

func TestRenderRelationMarkdown(t *testing.T) {
	r := RelationReport{
		EntitiesScanned: 2,
		MemoriesScanned: 5,
		Proposed: []RelationProposal{
			{SourceID: "d", SourceContent: "detail", TargetID: "b", TargetContent: "base", Type: "extends", Reason: "adds detail"},
		},
		Rejected: []RejectedRelation{
			{SourceID: "x", TargetID: "x", Type: "extends", Reason: "self-loop"},
		},
	}
	out := RenderRelationMarkdown(r)
	for _, want := range []string{"Relation", "extends", "detail", "base", "self-loop"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected output to contain %q, got:\n%s", want, out)
		}
	}

	skipped := RenderRelationMarkdown(RelationReport{Skipped: true, SkipReason: "no detector"})
	if !strings.Contains(skipped, "no detector") {
		t.Fatalf("expected skip reason in output, got:\n%s", skipped)
	}
}
