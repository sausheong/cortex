package vault

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sausheong/cortex"
)

// resolvedRel is a relationship with both ends resolved to vault paths
// and display names. The exporter resolves these once when building the
// in-memory page set; renderers consume them directly.
type resolvedRel struct {
	Type      string // e.g. "works_at"
	OtherPath string // e.g. "organizations/stripe" (no .md suffix)
	OtherName string // display name for wikilink alias
}

// renderEntity produces the markdown content of an entity page.
// sourceLinks is a list of already-resolved source basenames (no extension,
// no directory) ready to drop into [[sources/<basename>]] wikilinks. The
// caller (the exporter) is responsible for resolving raw source strings
// through any collision map before passing them in. Pass nil/empty slices
// to omit sections.
func renderEntity(
	e cortex.Entity,
	memories []cortex.Memory,
	outRels []resolvedRel,
	inRels []resolvedRel,
	sourceLinks []string,
	exportedAt time.Time,
) string {
	var b strings.Builder

	// Frontmatter.
	b.WriteString("---\n")
	fmt.Fprintf(&b, "cortex_id: %s\n", e.ID)
	fmt.Fprintf(&b, "type: %s\n", e.Type)
	fmt.Fprintf(&b, "name: %s\n", yamlString(e.Name))
	if e.Source != "" {
		fmt.Fprintf(&b, "source: %s\n", yamlString(e.Source))
	}
	fmt.Fprintf(&b, "created_at: %s\n", e.CreatedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "updated_at: %s\n", e.UpdatedAt.UTC().Format(time.RFC3339))
	if len(e.Attributes) > 0 {
		b.WriteString("attributes:\n")
		for _, k := range sortedKeys(e.Attributes) {
			fmt.Fprintf(&b, "  %s: %s\n", k, yamlString(fmt.Sprint(e.Attributes[k])))
		}
	}
	if e.Confidence != 0 && e.Confidence != 1.0 {
		fmt.Fprintf(&b, "confidence: %g\n", e.Confidence)
	}
	fmt.Fprintf(&b, "exported_at: %s\n", exportedAt.UTC().Format(time.RFC3339))
	b.WriteString("---\n\n")

	// Title.
	fmt.Fprintf(&b, "# %s\n", e.Name)

	// Sections. Each omitted if empty.
	if len(memories) > 0 {
		b.WriteString("\n## Memories\n\n")
		for _, m := range memories {
			var confSuffix string
			if m.Confidence > 0 && m.Confidence < 1.0 {
				confSuffix = fmt.Sprintf(" (conf %d%%)", int(m.Confidence*100))
			}
			if m.Source != "" {
				fmt.Fprintf(&b, "- %s%s — `%s`\n", m.Content, confSuffix, m.Source)
			} else {
				fmt.Fprintf(&b, "- %s%s\n", m.Content, confSuffix)
			}
		}
	}
	if len(outRels) > 0 {
		b.WriteString("\n## Relationships\n\n")
		for _, r := range outRels {
			fmt.Fprintf(&b, "- %s → [[%s|%s]]\n", r.Type, r.OtherPath, r.OtherName)
		}
	}
	if len(inRels) > 0 {
		b.WriteString("\n## Backlinks\n\n")
		for _, r := range inRels {
			fmt.Fprintf(&b, "- [[%s|%s]] — %s\n", r.OtherPath, r.OtherName, r.Type)
		}
	}
	if len(sourceLinks) > 0 {
		b.WriteString("\n## Sources\n\n")
		for _, link := range sourceLinks {
			fmt.Fprintf(&b, "- [[sources/%s]]\n", link)
		}
	}

	return b.String()
}

// yamlString wraps a value in double quotes if it contains characters
// that would confuse a YAML parser. For our purposes (single-line strings
// from entity names and attribute values), this catches the common cases
// without pulling in a YAML library.
func yamlString(s string) string {
	if needsYAMLQuoting(s) {
		// Escape backslashes and double quotes.
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		return `"` + s + `"`
	}
	return s
}

func needsYAMLQuoting(s string) bool {
	if s == "" {
		return true
	}
	if strings.ContainsAny(s, ":#&*!|>'\"%@`\n\r\t") {
		return true
	}
	// Leading/trailing whitespace or special leading chars.
	if s[0] == ' ' || s[0] == '-' || s[0] == '?' || s[len(s)-1] == ' ' {
		return true
	}
	return false
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sourcePage is the input to renderSource: one source identifier and
// the entities + memories it contributed to.
type sourcePage struct {
	Source   string
	Entities []sourceEntity
	Memories []string
}

type sourceEntity struct {
	Path string
	Name string
}

func renderSource(p sourcePage, exportedAt time.Time) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "source: %s\n", yamlString(p.Source))
	fmt.Fprintf(&b, "exported_at: %s\n", exportedAt.UTC().Format(time.RFC3339))
	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "# %s\n", p.Source)
	if len(p.Entities) > 0 {
		b.WriteString("\n## Entities\n\n")
		for _, e := range p.Entities {
			fmt.Fprintf(&b, "- [[%s|%s]]\n", e.Path, e.Name)
		}
	}
	if len(p.Memories) > 0 {
		b.WriteString("\n## Memories\n\n")
		for _, m := range p.Memories {
			fmt.Fprintf(&b, "- %s\n", m)
		}
	}
	return b.String()
}

// indexGroup is one section in index.md (e.g. all People entities).
type indexGroup struct {
	Heading string
	Items   []indexItem
}

type indexItem struct {
	Path    string
	Name    string
	Summary string // optional one-line description
}

func renderIndex(groups []indexGroup, exportedAt time.Time) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "exported_at: %s\n", exportedAt.UTC().Format(time.RFC3339))
	b.WriteString("---\n\n# Index\n")
	for _, g := range groups {
		fmt.Fprintf(&b, "\n## %s\n\n", g.Heading)
		for _, it := range g.Items {
			if it.Summary != "" {
				fmt.Fprintf(&b, "- [[%s|%s]] — %s\n", it.Path, it.Name, it.Summary)
			} else {
				fmt.Fprintf(&b, "- [[%s|%s]]\n", it.Path, it.Name)
			}
		}
	}
	b.WriteString("\n---\n\nSee also: [[log]]\n")
	return b.String()
}

// formatLogLine builds a single log.md entry. Always terminated with \n.
func formatLogLine(ts time.Time, op, summary string) string {
	return fmt.Sprintf("## [%s] %s | %s\n", ts.UTC().Format(time.RFC3339), op, summary)
}
