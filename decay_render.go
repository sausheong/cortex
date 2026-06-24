package cortex

import (
	"fmt"
	"strings"
)

// RenderDecayMarkdown renders a DecayReport as human-readable markdown.
func RenderDecayMarkdown(r DecayReport) string {
	var b strings.Builder
	b.WriteString("# Decay Report\n\n")
	if r.DryRun {
		b.WriteString("_Dry run — no changes written._\n\n")
	}
	fmt.Fprintf(&b, "Scanned: %d\n", r.Scanned)
	fmt.Fprintf(&b, "Decayed: %d\n", r.Decayed)
	fmt.Fprintf(&b, "Pruned: %d\n\n", r.Pruned)

	if len(r.Changes) > 0 {
		b.WriteString("## Changes\n\n")
		for _, ch := range r.Changes {
			pruned := ""
			if ch.Pruned {
				pruned = " [PRUNED]"
			}
			fmt.Fprintf(&b, "- %q (%s): %.3f → %.3f%s\n",
				ch.Content, ch.ID, ch.OldConfidence, ch.NewConfidence, pruned)
		}
	}
	return b.String()
}
