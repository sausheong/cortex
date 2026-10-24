package web

import (
	"bytes"
	"html"
	"html/template"

	"github.com/sausheong/cortex/internal/web/pages"

	"regexp"
	"strings"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// wikiLink is an Obsidian [[target|alias]] link, resolved against the viewer's
// own vault when parsed.
type wikiLink struct {
	ast.BaseInline
	Label string
	Href  string // "" when the target has no page
}

var kindWikiLink = ast.NewNodeKind("WikiLink")

func (*wikiLink) Kind() ast.NodeKind           { return kindWikiLink }
func (n *wikiLink) Dump(src []byte, level int) { ast.DumpHelper(n, src, level, nil, nil) }

type wikiParser struct {
	resolve func(target string) (path string, ok bool)
}

func (wikiParser) Trigger() []byte { return []byte{'['} }

func (p wikiParser) Parse(_ ast.Node, block text.Reader, _ parser.Context) ast.Node {
	line, _ := block.PeekLine()
	if len(line) < 5 || line[1] != '[' {
		return nil
	}
	end := bytes.Index(line, []byte("]]"))
	if end < 3 {
		return nil
	}
	inner := string(line[2:end])
	target, alias, hasAlias := strings.Cut(inner, "|")
	target = strings.TrimSpace(target)
	if target == "" || strings.ContainsAny(target, "[]") {
		return nil
	}
	label := strings.TrimSpace(alias)
	if !hasAlias || label == "" {
		label = target
	}
	block.Advance(end + 2)
	n := &wikiLink{Label: label}
	if path, ok := p.resolve(target); ok {
		n.Href = "/vault/" + escapePath(path)
	}
	return n
}

func escapePath(p string) string { return pages.EscapePath(p) }

type wikiRenderer struct{}

func (wikiRenderer) RegisterFuncs(r renderer.NodeRendererFuncRegisterer) {
	r.Register(kindWikiLink, func(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		n := node.(*wikiLink)
		label := util.EscapeHTML([]byte(n.Label))
		if n.Href == "" {
			_, _ = w.WriteString(`<span class="unresolved" title="No page with this name">`)
			_, _ = w.Write(label)
			_, _ = w.WriteString(`</span>`)
		} else {
			_, _ = w.WriteString(`<a class="wikilink" href="`)
			_, _ = w.Write(util.EscapeHTML([]byte(n.Href)))
			_, _ = w.WriteString(`">`)
			_, _ = w.Write(label)
			_, _ = w.WriteString(`</a>`)
		}
		return ast.WalkContinue, nil
	})
}

// safeURL reports whether a link destination is http(s), mailto or relative.
// Entities and control characters are decoded first, because browsers do the
// same before choosing a scheme (goldmark checks before decoding).
func safeURL(dest string) bool {
	d := html.UnescapeString(dest)
	d = strings.Map(func(r rune) rune {
		if r <= 0x20 || r == 0x7f || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, d)
	d = strings.ToLower(d)
	i := strings.IndexAny(d, ":/?#")
	if i < 0 || d[i] != ':' {
		return true // relative
	}
	switch d[:i] {
	case "http", "https", "mailto":
		return true
	}
	return false
}

// sanitizer is an AST transformer: images become plain links (no remote
// loads from memory text) and unsafe destinations are dropped.
type sanitizer struct{}

func (sanitizer) Transform(doc *ast.Document, reader text.Reader, _ parser.Context) {
	src := reader.Source()
	var imgs []*ast.Image
	var autos []*ast.AutoLink
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch t := n.(type) {
		case *ast.Image:
			imgs = append(imgs, t)
		case *ast.AutoLink:
			if !safeURL(string(t.URL(src))) {
				autos = append(autos, t)
			}
		case *ast.Link:
			if !safeURL(string(t.Destination)) {
				t.Destination = []byte("#")
			}
		}
		return ast.WalkContinue, nil
	})
	for _, a := range autos { // unsafe autolinks become inert text
		a.Parent().ReplaceChild(a.Parent(), a, ast.NewString(a.Label(src)))
	}
	for _, im := range imgs {
		l := ast.NewLink()
		l.Destination, l.Title = im.Destination, im.Title
		if !safeURL(string(l.Destination)) {
			l.Destination = []byte("#")
		}
		for c := im.FirstChild(); c != nil; c = im.FirstChild() {
			l.AppendChild(l, c)
		}
		if im.FirstChild() == nil && l.FirstChild() == nil {
			l.AppendChild(l, ast.NewString([]byte("image")))
		}
		im.Parent().ReplaceChild(im.Parent(), im, l)
	}
}

// renderMarkdown turns vault markdown into HTML. Raw HTML in the source is
// dropped and unsafe link schemes (javascript:, data:, ...) are neutralised
// by goldmark's default, safe configuration (no html.WithUnsafe).
func renderMarkdown(src string, resolve func(string) (string, bool)) template.HTML {
	md := goldmark.New(
		goldmark.WithParserOptions(parser.WithInlineParsers(
			util.Prioritized(wikiParser{resolve}, 199)),
			parser.WithASTTransformers(util.Prioritized(sanitizer{}, 100))),
		goldmark.WithRendererOptions(renderer.WithNodeRenderers(
			util.Prioritized(wikiRenderer{}, 100))),
	)
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(src))
	}
	// Plain links with a scheme we do not browse to are rendered inert by
	// goldmark; links that remain get rel so they never leak the vault URL.
	// Every markdown-produced anchor starts `<a href="` (wikilinks carry a
	// class first and are internal), so rel is set on all of them.
	out := strings.ReplaceAll(buf.String(), `<a href="`, `<a rel="noopener noreferrer nofollow" href="`)
	return template.HTML(out)
}

// splitFrontmatter separates the leading --- block from the body.
func splitFrontmatter(content string) (front, body string) {
	if !strings.HasPrefix(content, "---\n") {
		return "", content
	}
	rest := content[4:]
	i := strings.Index(rest, "\n---\n")
	if i < 0 {
		return "", content
	}
	return rest[:i], strings.TrimLeft(rest[i+5:], "\n")
}

type prop struct {
	Key, Value string
	Nested     bool
	Label      string      // display label
	When       *exportedAt // set for timestamps
}

var kvLine = regexp.MustCompile(`^(\s*)([A-Za-z0-9_.-]+):(?: (.*))?$`)

// parseProps reads the exporter's flat frontmatter (one level of nesting
// under attributes) into display rows.
func parseProps(front string) []prop {
	var out []prop
	for _, ln := range strings.Split(front, "\n") {
		m := kvLine.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		v := unyaml(m[3])
		if m[3] == "" && m[1] == "" {
			out = append(out, prop{Key: m[2]})
			continue
		}
		out = append(out, prop{Key: m[2], Value: v, Nested: m[1] != ""})
	}
	return out
}

func unyaml(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
		s = strings.ReplaceAll(s, `\"`, `"`)
		s = strings.ReplaceAll(s, `\\`, `\`)
	}
	return s
}

var wikiRe = regexp.MustCompile(`\[\[([^\[\]\n]+?)\]\]`)

// wikiTargets lists the link targets in content, in order.
func wikiTargets(content string) []string {
	var out []string
	for _, m := range wikiRe.FindAllStringSubmatch(content, -1) {
		t, _, _ := strings.Cut(m[1], "|")
		out = append(out, strings.TrimSpace(t))
	}
	return out
}
