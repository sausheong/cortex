package cortex

import (
	"fmt"
	"strings"
)

// RenderExpireMarkdown renders an ExpireReport as human-readable markdown,
// mirroring RenderDecayMarkdown.
func RenderExpireMarkdown(r ExpireReport) string {
	var b strings.Builder
	b.WriteString("# Expire Report\n\n")
	if r.DryRun {
		b.WriteString("_Dry run — no changes written._\n\n")
	}
	fmt.Fprintf(&b, "Scanned: %d\n", r.Scanned)
	fmt.Fprintf(&b, "Expired: %d\n\n", r.Expired)

	if len(r.Changes) > 0 {
		b.WriteString("## Expired\n\n")
		for _, ch := range r.Changes {
			fmt.Fprintf(&b, "- %q (%s): forget_after %s\n",
				ch.Content, ch.ID, ch.ForgetAfter.Format("2006-01-02"))
		}
	}
	return b.String()
}
