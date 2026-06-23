package cortex

import (
	"fmt"
	"strings"
)

// RenderReconcileMarkdown renders a ReconcileReport as a human-readable
// markdown summary, mirroring RenderLintMarkdown. It uses dry-run wording
// ("Proposed", "Rejected by gate"); use RenderReconcileMarkdownMode to render
// the applied-path wording.
func RenderReconcileMarkdown(r ReconcileReport) string {
	return RenderReconcileMarkdownMode(r, false)
}

// RenderReconcileMarkdownMode renders a ReconcileReport as a human-readable
// markdown summary. When applied is false the report describes a dry-run
// ("Proposed Supersessions", "Rejected by gate"); when applied is true the
// report describes the result of an apply run ("Applied Supersessions",
// "Skipped (re-validation)"), where r.Proposed lists what was applied and
// r.Rejected lists re-validation skips.
func RenderReconcileMarkdownMode(r ReconcileReport, applied bool) string {
	var b strings.Builder
	b.WriteString("# Reconcile Report\n\n")

	if r.Skipped {
		fmt.Fprintf(&b, "Skipped: %s\n", r.SkipReason)
		return b.String()
	}

	fmt.Fprintf(&b, "Entities scanned: %d\n", r.EntitiesScanned)
	fmt.Fprintf(&b, "Memories scanned: %d\n", r.MemoriesScanned)
	if applied {
		fmt.Fprintf(&b, "Applied: %d\n", len(r.Proposed))
		fmt.Fprintf(&b, "Skipped (re-validation): %d\n\n", len(r.Rejected))
	} else {
		fmt.Fprintf(&b, "Proposed supersessions: %d\n", len(r.Proposed))
		fmt.Fprintf(&b, "Rejected (gate): %d\n\n", len(r.Rejected))
	}

	if len(r.Proposed) > 0 {
		if applied {
			b.WriteString("## Applied Supersessions\n\n")
		} else {
			b.WriteString("## Proposed Supersessions\n\n")
		}
		for _, p := range r.Proposed {
			fmt.Fprintf(&b, "- **stale** %q (%s)\n", p.StaleContent, p.StaleID)
			fmt.Fprintf(&b, "  **superseded by** %q (%s)\n", p.SupersededByContent, p.SupersededByID)
			fmt.Fprintf(&b, "  reason: %s; invalid_at: %s\n\n", p.Reason, p.InvalidAt.Format("2006-01-02"))
		}
	}

	if len(r.Rejected) > 0 {
		if applied {
			b.WriteString("## Skipped (re-validation)\n\n")
		} else {
			b.WriteString("## Rejected by gate\n\n")
		}
		for _, rj := range r.Rejected {
			fmt.Fprintf(&b, "- %s -> %s: %s\n", rj.StaleID, rj.SupersededByID, rj.Reason)
		}
	}

	return b.String()
}
