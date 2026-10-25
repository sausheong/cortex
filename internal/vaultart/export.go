package vaultart

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
	"github.com/sausheong/cortex/internal/workset"
)

// ExportFile is one file of the Obsidian download.
type ExportFile struct {
	Path     string
	Content  string
	Modified time.Time
}

// StubNote is the line a not-yet-written article carries in the export.
const StubNote = "This article has not been written yet. Open it in Cortex to write it."

// BuildExport renders the user's Vault as Obsidian markdown: one file per
// qualifying subject plus index.md. A written article is exported as stored
// (re-validated against the current data); an unwritten subject becomes a stub
// that lists its live memories. It never calls the article writer, so a
// download costs no model tokens. Entity names and memory text are user data
// and appear as written; everything cortex writes itself, and the validated
// article prose, carries no em dash.
func BuildExport(ctx context.Context, h *store.Handle, set *workset.Set, ownerID string, s Settings, now time.Time) ([]ExportFile, error) {
	ix, err := BuildIndex(ctx, h, set, ownerID, s, now)
	if err != nil {
		return nil, err
	}
	rel := ix.relIndex()
	var files []ExportFile
	sections := []struct {
		heading string
		rows    []IndexEntry
	}{
		{"People", ix.People},
		{"Projects and organisations", ix.Projects},
		{"Events", ix.Events},
		{"Topics", ix.Topics},
	}
	for _, sec := range sections {
		for _, e := range sec.rows {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			f, err := exportOne(ctx, h, ix, rel, e, now)
			if err != nil {
				return nil, err
			}
			files = append(files, f)
		}
	}

	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("title: " + yamlString("Cortex vault") + "\n")
	b.WriteString("exported: " + yamlString(now.UTC().Format("2006-01-02")) + "\n")
	b.WriteString("---\n\n# Cortex vault\n\n")
	any := false
	for _, sec := range sections {
		if len(sec.rows) == 0 {
			continue
		}
		any = true
		b.WriteString("## " + sec.heading + "\n\n")
		for _, e := range sec.rows {
			b.WriteString("- " + wikilink(e) + " " + mdText(paren(e.Descriptor), false))
			if e.WrittenAt == nil {
				b.WriteString(", not written yet")
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if !any {
		b.WriteString("Nothing qualifies for an article yet.\n")
	}
	files = append(files, ExportFile{Path: "index.md", Content: b.String(), Modified: now})
	return files, nil
}

func paren(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return "(" + s + ")"
}

func exportOne(ctx context.Context, h *store.Handle, ix *Index, rel *index, e IndexEntry, now time.Time) (ExportFile, error) {
	sub := e.Subject
	path := strings.TrimPrefix(e.Path, "/vault/") + ".md"
	var row *store.ArticleRow
	if e.WrittenAt != nil {
		r, err := h.GetArticle(ctx, sub.ID)
		switch {
		case err == nil:
			row = r
		case errors.Is(err, store.ErrArticleCorrupt):
			// Falls back to the stub: the page rewrites it on next open.
		default:
			return ExportFile{}, err
		}
	}
	mems := rel.memories(sub)
	title := e.Name

	if row != nil {
		var opts []Option
		if sub.Kind == "topic" {
			opts = append(opts, ForTopic())
		}
		valid := map[string]bool{}
		for _, m := range mems {
			valid[m.ID] = true
		}
		rd, err := Validate(row.Article, valid, ix.Linkables(sub), opts...)
		if err == nil {
			return ExportFile{Path: path, Content: writtenMarkdown(ix.set, ix, e, title, rd, len(mems), row.WrittenAt), Modified: row.WrittenAt}, nil
		}
	}
	mod := now
	for _, m := range mems {
		if m.CreatedAt.After(mod) || mod.Equal(now) {
			mod = m.CreatedAt
		}
	}
	return ExportFile{Path: path, Content: stubMarkdown(e, title, mems), Modified: mod}, nil
}

// frontmatter writes the YAML block. Every value is a double-quoted string
// from yamlString, so a hostile title cannot add keys or end the block.
func frontmatter(kv ...string) string {
	var b strings.Builder
	b.WriteString("---\n")
	for i := 0; i+1 < len(kv); i += 2 {
		b.WriteString(kv[i] + ": " + yamlString(kv[i+1]) + "\n")
	}
	b.WriteString("---\n\n")
	return b.String()
}

// yamlString returns s as a double-quoted YAML scalar.
func yamlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	s = strings.ToValidUTF8(s, "�")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '"':
			b.WriteString(`\"`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == 0x85:
			b.WriteString(`\N`)
		case r == 0x2028:
			b.WriteString(`\L`)
		case r == 0x2029:
			b.WriteString(`\P`)
		case r == 0xFEFF:
			b.WriteString(`\uFEFF`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// mdText makes user or model text safe as one line of markdown: newlines are
// flattened, brackets, backticks, backslashes and angle brackets cannot form
// wikilinks, footnotes, code or HTML, and with lineStart a leading block
// marker is escaped.
func mdText(s string, lineStart bool) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ' ' || r == ' ' || r == 0x85 || r == '\t' || r == 0x0b || r == 0x0c
	}), " ")
	s = strings.Join(strings.Fields(s), " ")
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '[':
			b.WriteString(`\[`)
		case ']':
			b.WriteString(`\]`)
		case '`':
			b.WriteString("\\`")
		case '<':
			b.WriteString("&lt;")
		default:
			if r < 0x20 || r == 0x7f {
				continue
			}
			b.WriteRune(r)
		}
	}
	out := b.String()
	if lineStart && out != "" {
		out = escapeLineStart(out)
	}
	return out
}

func escapeLineStart(s string) string {
	r, _ := utf8.DecodeRuneInString(s)
	switch r {
	case '#', '>', '-', '+', '*', '_', '=', '~', ':', '!':
		return `\` + s
	}
	if unicode.IsDigit(r) {
		i := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i < len(s) && (s[i] == '.' || s[i] == ')') {
			return s[:i] + `\` + s[i:]
		}
	}
	return s
}

// aliasText is a name made safe as a wikilink alias.
func aliasText(s string) string {
	s = strings.Join(strings.Fields(strings.NewReplacer("[", " ", "]", " ", "|", " ", "\\", " ", "`", " ", "<", " ", ">", " ").Replace(s)), " ")
	if s == "" {
		return "Untitled"
	}
	return s
}

func wikilink(e IndexEntry) string {
	return "[[" + strings.TrimPrefix(e.Path, "/vault/") + "|" + aliasText(e.Name) + "]]"
}

func linkOf(t LinkTarget, text string) string {
	return "[[" + string(t.Group) + "/" + t.Slug + "|" + aliasText(text) + "]]"
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// findFold finds name in text ignoring case at word boundaries and returns its
// byte range, or -1.
func findFold(text, name string) (int, int) {
	n := utf8.RuneCountInString(name)
	if n == 0 {
		return -1, -1
	}
	for i := range text {
		j, c := i, 0
		for c < n && j < len(text) {
			_, sz := utf8.DecodeRuneInString(text[j:])
			j += sz
			c++
		}
		if c < n {
			break
		}
		if !strings.EqualFold(text[i:j], name) {
			continue
		}
		if p, _ := utf8.DecodeLastRuneInString(text[:i]); i > 0 && isWordRune(p) {
			continue
		}
		if q, _ := utf8.DecodeRuneInString(text[j:]); j < len(text) && isWordRune(q) {
			continue
		}
		return i, j
	}
	return -1, -1
}

// linkedProse places a wikilink at the first whole-word mention of each target
// and escapes every other run.
func linkedProse(text string, targets []LinkTarget, links map[string]LinkTarget) string {
	type hit struct {
		a, b int
		t    LinkTarget
	}
	var hits []hit
	for _, t := range targets {
		for _, name := range NamesFor(links, t) {
			a, b := findFold(text, name)
			if a < 0 {
				continue
			}
			hits = append(hits, hit{a, b, t})
			break
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].a < hits[j].a })
	var out strings.Builder
	pos := 0
	first := true
	plain := func(s string) {
		if s == "" {
			return
		}
		lead := !first && unicode.IsSpace([]rune(s)[0])
		trail := unicode.IsSpace([]rune(s)[len([]rune(s))-1])
		t := mdText(s, first)
		if t == "" {
			if lead || trail {
				out.WriteString(" ")
			}
			return
		}
		if lead {
			out.WriteString(" ")
		}
		out.WriteString(t)
		if trail {
			out.WriteString(" ")
		}
		first = false
	}
	for _, h := range hits {
		if h.a < pos {
			continue
		}
		plain(text[pos:h.a])
		out.WriteString(linkOf(h.t, text[h.a:h.b]))
		first = false
		pos = h.b
	}
	plain(text[pos:])
	return out.String()
}

func marksText(ns []int) string {
	var b strings.Builder
	for _, n := range ns {
		fmt.Fprintf(&b, "[^%d]", n)
	}
	return b.String()
}

func writtenMarkdown(set *workset.Set, ix *Index, e IndexEntry, title string, rd Rendered, nmem int, written time.Time) string {
	links := ix.Linkables(e.Subject)
	var b strings.Builder
	b.WriteString(frontmatter(
		"title", title,
		"type", e.Descriptor,
		"group", string(e.Group),
		"status", "written",
		"written", written.UTC().Format("2006-01-02"),
		"memories", fmt.Sprint(nmem),
	))
	b.WriteString("# " + mdText(title, false) + "\n\n")
	if e.Descriptor != "" && e.Descriptor != title {
		b.WriteString("*" + mdText(e.Descriptor, false) + "*\n\n")
	}
	item := func(text string, ms []int, ls []LinkTarget) string {
		s := linkedProse(text, ls, links)
		if len(ms) > 0 {
			s += marksText(ms)
		}
		return s
	}
	if rd.Lead != "" {
		b.WriteString(item(rd.Lead, rd.LeadMarkers, nil) + "\n\n")
	}
	for _, sec := range rd.Sections {
		b.WriteString("## " + mdText(sec.Heading, false) + "\n\n")
		prevBullet := false
		for i, it := range sec.Items {
			line := item(it.Text, it.Markers, it.Links)
			if it.Bullet {
				b.WriteString("- " + line + "\n")
			} else {
				if i > 0 && prevBullet {
					b.WriteString("\n")
				}
				b.WriteString(line + "\n\n")
			}
			prevBullet = it.Bullet
		}
		if prevBullet {
			b.WriteString("\n")
		}
	}
	if len(rd.Footnotes) > 0 {
		for i, id := range rd.Footnotes {
			b.WriteString(fmt.Sprintf("[^%d]: ", i+1))
			if m, ok := set.MemoryByID(id); ok {
				b.WriteString(memLine(m))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if rel := ix.Related(e.Subject, 8); len(rel) > 0 {
		b.WriteString("## Related\n\n")
		for _, r := range rel {
			b.WriteString("- " + wikilink(r) + "\n")
		}
		b.WriteString("\n")
	}
	if also := ix.AlsoMentioned(e.Subject); len(also) > 0 {
		esc := make([]string, len(also))
		for i, n := range also {
			esc[i] = mdText(n, false)
		}
		b.WriteString("Also mentioned: " + strings.Join(esc, ", ") + "\n")
	}
	return b.String()
}

// memLine is a memory as one escaped line with its date.
func memLine(m model.Memory) string {
	return mdText(m.Content, false) + " (" + m.CreatedAt.UTC().Format("2006-01-02") + ")"
}

func stubMarkdown(e IndexEntry, title string, mems []*model.Memory) string {
	var b strings.Builder
	b.WriteString(frontmatter(
		"title", title,
		"type", e.Descriptor,
		"group", string(e.Group),
		"status", "not written",
		"memories", fmt.Sprint(len(mems)),
	))
	b.WriteString("# " + mdText(title, false) + "\n\n")
	b.WriteString(StubNote + "\n\n")
	sorted := append([]*model.Memory(nil), mems...)
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].CreatedAt.Equal(sorted[j].CreatedAt) {
			return sorted[i].CreatedAt.Before(sorted[j].CreatedAt)
		}
		return sorted[i].ID < sorted[j].ID
	})
	if len(sorted) > 0 {
		b.WriteString("## Memories\n\n")
		for _, m := range sorted {
			b.WriteString("- " + memLine(*m) + "\n")
		}
	}
	return b.String()
}
