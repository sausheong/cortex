package cortex

import (
	"fmt"
	"strings"
)

// renderLintMarkdown formats a LintReport as a human-readable markdown
// document. Empty finding sections are omitted entirely. If
// LowConfidenceMemories is nil (i.e. the option was not passed), a
// "skipped" note appears in its place.
func renderLintMarkdown(r LintReport) string {
	var b strings.Builder
	b.WriteString("# Cortex Lint Report\n\n")
	fmt.Fprintf(&b, "Scanned %d entities, %d relationships, %d memories.\n",
		r.EntityCount, r.RelationshipCount, r.MemoryCount)

	if len(r.Orphans) > 0 {
		fmt.Fprintf(&b, "\n## Orphan entities (%d)\n\n", len(r.Orphans))
		b.WriteString("Entities with no relationships and no memory links — likely noise.\n\n")
		for _, e := range r.Orphans {
			fmt.Fprintf(&b, "- `%s` — %q (%s)\n", e.ID, e.Name, e.Type)
		}
	}

	if len(r.EntitiesNoMemories) > 0 {
		fmt.Fprintf(&b, "\n## Entities without memories (%d)\n\n", len(r.EntitiesNoMemories))
		b.WriteString("Entities with relationships but no memory links.\n\n")
		for _, e := range r.EntitiesNoMemories {
			fmt.Fprintf(&b, "- `%s` — %q (%s)\n", e.ID, e.Name, e.Type)
		}
	}

	if len(r.NearDuplicates) > 0 {
		word := "pairs"
		if len(r.NearDuplicates) == 1 {
			word = "pair"
		}
		fmt.Fprintf(&b, "\n## Near-duplicate entity names (%d %s)\n\n", len(r.NearDuplicates), word)
		b.WriteString("Same type + case-insensitively-equal name. Consider `cortex merge`.\n\n")
		for _, p := range r.NearDuplicates {
			fmt.Fprintf(&b, "- %q / %q (%s): `%s` / `%s`\n",
				p.A.Name, p.B.Name, p.Type, p.A.ID, p.B.ID)
		}
	}

	if len(r.DeadSources) > 0 {
		fmt.Fprintf(&b, "\n## Dead sources (%d)\n\n", len(r.DeadSources))
		b.WriteString("Source values present on memory rows but no live entity carries them.\n\n")
		for _, s := range r.DeadSources {
			fmt.Fprintf(&b, "- `%s`\n", s)
		}
	}

	if len(r.UnlinkedMemories) > 0 {
		fmt.Fprintf(&b, "\n## Memories with no entity links (%d)\n\n", len(r.UnlinkedMemories))
		b.WriteString("These memories are findable via search but not via graph traversal.\n\n")
		for _, m := range r.UnlinkedMemories {
			if m.Source != "" {
				fmt.Fprintf(&b, "- `%s` — %q (source: %s)\n", m.ID, m.Content, m.Source)
			} else {
				fmt.Fprintf(&b, "- `%s` — %q\n", m.ID, m.Content)
			}
		}
	}

	if r.LowConfidenceMemories == nil {
		b.WriteString("\n## Low-confidence memories (skipped — pass --low-confidence to include)\n")
	} else if len(r.LowConfidenceMemories) > 0 {
		fmt.Fprintf(&b, "\n## Low-confidence memories (%d)\n\n", len(r.LowConfidenceMemories))
		b.WriteString("Memories the LLM was uncertain about. Worth reviewing.\n\n")
		for _, m := range r.LowConfidenceMemories {
			fmt.Fprintf(&b, "- `%s` (conf %.0f%%) — %q\n", m.ID, m.Confidence*100, m.Content)
		}
	}

	return b.String()
}

// RenderLintMarkdown formats a LintReport as a human-readable markdown
// document. Exported wrapper for CLI consumption.
func RenderLintMarkdown(r LintReport) string {
	return renderLintMarkdown(r)
}
