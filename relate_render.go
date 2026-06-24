package cortex

import (
	"fmt"
	"strings"
)

// RenderRelationMarkdown renders a RelationReport as human-readable markdown,
// mirroring RenderReconcileMarkdown.
func RenderRelationMarkdown(r RelationReport) string {
	var b strings.Builder
	b.WriteString("# Relation Report\n\n")

	if r.Skipped {
		fmt.Fprintf(&b, "Skipped: %s\n", r.SkipReason)
		return b.String()
	}

	fmt.Fprintf(&b, "Entities scanned: %d\n", r.EntitiesScanned)
	fmt.Fprintf(&b, "Memories scanned: %d\n", r.MemoriesScanned)
	fmt.Fprintf(&b, "Proposed relations: %d\n", len(r.Proposed))
	fmt.Fprintf(&b, "Rejected (gate): %d\n\n", len(r.Rejected))

	if len(r.Proposed) > 0 {
		b.WriteString("## Proposed Relations\n\n")
		for _, p := range r.Proposed {
			fmt.Fprintf(&b, "- %q (%s) **%s** %q (%s)\n", p.SourceContent, p.SourceID, p.Type, p.TargetContent, p.TargetID)
			fmt.Fprintf(&b, "  reason: %s\n\n", p.Reason)
		}
	}

	if len(r.Rejected) > 0 {
		b.WriteString("## Rejected by gate\n\n")
		for _, rj := range r.Rejected {
			fmt.Fprintf(&b, "- %s %s %s: %s\n", rj.SourceID, rj.Type, rj.TargetID, rj.Reason)
		}
	}

	return b.String()
}
