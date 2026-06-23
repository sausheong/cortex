package cortex

import (
	"fmt"
	"strings"
)

// RenderReconcileMarkdown renders a ReconcileReport as a human-readable
// markdown summary, mirroring RenderLintMarkdown.
func RenderReconcileMarkdown(r ReconcileReport) string {
	var b strings.Builder
	b.WriteString("# Reconcile Report\n\n")

	if r.Skipped {
		fmt.Fprintf(&b, "Skipped: %s\n", r.SkipReason)
		return b.String()
	}

	fmt.Fprintf(&b, "Entities scanned: %d\n", r.EntitiesScanned)
	fmt.Fprintf(&b, "Memories scanned: %d\n", r.MemoriesScanned)
	fmt.Fprintf(&b, "Proposed supersessions: %d\n", len(r.Proposed))
	fmt.Fprintf(&b, "Rejected (gate): %d\n\n", len(r.Rejected))

	if len(r.Proposed) > 0 {
		b.WriteString("## Proposed Supersessions\n\n")
		for _, p := range r.Proposed {
			fmt.Fprintf(&b, "- **stale** %q (%s)\n", p.StaleContent, p.StaleID)
			fmt.Fprintf(&b, "  **superseded by** %q (%s)\n", p.SupersededByContent, p.SupersededByID)
			fmt.Fprintf(&b, "  reason: %s; invalid_at: %s\n\n", p.Reason, p.InvalidAt.Format("2006-01-02"))
		}
	}

	if len(r.Rejected) > 0 {
		b.WriteString("## Rejected by gate\n\n")
		for _, rj := range r.Rejected {
			fmt.Fprintf(&b, "- %s -> %s: %s\n", rj.StaleID, rj.SupersededByID, rj.Reason)
		}
	}

	return b.String()
}
