package cortex

import (
	"strings"
)

// RenderMaintainMarkdown renders a MaintainReport by composing the per-pass
// renders under a single header. Sub-passes toggled off (nil) are noted as
// skipped.
func RenderMaintainMarkdown(r MaintainReport) string {
	var b strings.Builder
	b.WriteString("# Maintain Report\n\n")
	if r.DryRun {
		b.WriteString("_Dry run — no changes written._\n\n")
	}

	b.WriteString("## Reconcile\n\n")
	if r.Reconcile != nil {
		// applied=true when not a dry-run (ApplyReconcile path).
		b.WriteString(RenderReconcileMarkdownMode(*r.Reconcile, !r.DryRun))
	} else {
		b.WriteString("_skipped_\n")
	}
	b.WriteString("\n")

	b.WriteString("## Relate\n\n")
	if r.Relate != nil {
		b.WriteString(RenderRelationMarkdown(*r.Relate))
	} else {
		b.WriteString("_skipped_\n")
	}
	b.WriteString("\n")

	b.WriteString("## Decay\n\n")
	if r.Decay != nil {
		b.WriteString(RenderDecayMarkdown(*r.Decay))
	} else {
		b.WriteString("_skipped_\n")
	}

	return b.String()
}
