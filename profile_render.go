package cortex

import (
	"fmt"
	"strings"
)

// RenderProfileMarkdown renders a Profile as a human-readable markdown digest.
func RenderProfileMarkdown(p Profile) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Profile: %s\n\n", p.Name)
	if !p.BuiltAt.IsZero() {
		mode := "raw"
		if p.Distilled {
			mode = "distilled"
		}
		cached := "rebuilt"
		if p.Cached {
			cached = "cached"
		}
		fmt.Fprintf(&b, "_built %s (%s, %s)_\n\n", p.BuiltAt.Format("2006-01-02"), mode, cached)
	}

	b.WriteString("## Static\n\n")
	writeBullets(&b, p.Static)
	b.WriteString("\n## Dynamic\n\n")
	writeBullets(&b, p.Dynamic)
	return b.String()
}

func writeBullets(b *strings.Builder, lines []string) {
	if len(lines) == 0 {
		b.WriteString("_none_\n")
		return
	}
	for _, l := range lines {
		fmt.Fprintf(b, "- %s\n", l)
	}
}

// RenderProfileReportMarkdown renders a ProfileReport (the Maintain pass
// summary).
func RenderProfileReportMarkdown(r ProfileReport) string {
	var b strings.Builder
	b.WriteString("# Profile Report\n\n")
	fmt.Fprintf(&b, "Scanned: %d\n", r.Scanned)
	fmt.Fprintf(&b, "Rebuilt: %d\n", r.Rebuilt)
	fmt.Fprintf(&b, "Skipped: %d\n", len(r.Skipped))
	if len(r.Skipped) > 0 {
		b.WriteString("\n## Skipped\n\n")
		for _, id := range r.Skipped {
			fmt.Fprintf(&b, "- %s\n", id)
		}
	}
	return b.String()
}
