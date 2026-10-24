package pages

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestCurrent(t *testing.T) {
	cases := []struct {
		path, href string
		want       bool
	}{
		{"/memories", "/memories", true},
		{"/memories/delete", "/memories", true},
		{"/memories/deleted", "/memories", true},
		{"/compaction", "/memories", true},
		{"/compaction/abc", "/memories", true},
		{"/compaction/abc", "/settings", false},
		{"/connections", "/settings", false},
	}
	for _, c := range cases {
		got := Page{Path: c.path}.Current(c.href) != ""
		if got != c.want {
			t.Errorf("Current(%q) on %q = %v, want %v", c.href, c.path, got, c.want)
		}
	}
}

func TestCurrentSub(t *testing.T) {
	cases := []struct {
		path, href string
		want       bool
	}{
		{"/memories", "/memories", true},
		{"/memories/delete", "/memories", true},
		{"/memories/deleted", "/memories", false},
		{"/memories/deleted", "/memories/deleted", true},
		{"/compaction/abc", "/compaction", true},
		{"/compaction", "/memories", false},
	}
	for _, c := range cases {
		got := Page{Path: c.path}.CurrentSub(c.href) != ""
		if got != c.want {
			t.Errorf("CurrentSub(%q) on %q = %v, want %v", c.href, c.path, got, c.want)
		}
	}
}

var (
	inlineScript = regexp.MustCompile(`<script\b[^>]*>`)
	eventAttr    = regexp.MustCompile(`(?i)<[a-z][^>]*\son[a-z]+\s*=`)
)

// Every inline script carries the per-response CSP nonce, and no template uses
// inline event-handler attributes (which a nonce cannot authorise).
func TestTemplatesInlineScriptsCarryNonce(t *testing.T) {
	ents, err := files.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		b, _ := files.ReadFile(e.Name())
		for _, m := range inlineScript.FindAllString(string(b), -1) {
			if strings.Contains(m, " src=") {
				continue
			}
			if !strings.Contains(m, `nonce="{{$.Nonce}}"`) {
				t.Errorf("%s: inline script without nonce: %s", e.Name(), m)
			}
		}
		if m := eventAttr.FindString(string(b)); m != "" {
			t.Errorf("%s: inline event handler: %s", e.Name(), m)
		}
		if strings.Contains(string(b), "style=") || strings.Contains(string(b), "<style") {
			t.Errorf("%s: inline style", e.Name())
		}
	}
}

// Vault hrefs percent-encode each path segment, so a name with ?, # or %
// still opens the right page.
func TestVaultHrefsEscapePath(t *testing.T) {
	const path = "people/a b?c#d%e.md"
	const want = `/vault/people/a%20b%3Fc%23d%25e.md`
	run := func(name string, data any) string {
		var b strings.Builder
		if err := Load()[name].Execute(&b, Page{Data: data, SignedIn: true}); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	list := run("vault", map[string]any{"Rows": []map[string]any{{"Path": path, "Name": "x"}}})
	if !strings.Contains(list, `href="`+want+`"`) {
		t.Errorf("list href not escaped: %s", list)
	}
	page := run("vault_page", map[string]any{"Path": path, "Title": "t",
		"Backlinks": []map[string]any{{"Path": path, "Name": "n"}}})
	if got := strings.Count(page, `href="`+want+`"`); got != 2 {
		t.Errorf("page: %d escaped hrefs, want 2 (preview, backlink): %s", got, page)
	}
	if !strings.Contains(page, `href="`+want+`?view=source"`) {
		t.Errorf("source href not escaped")
	}
}

// The unlock and enroll pages exist, render for the layout, carry no inline
// style or handler, and use no em dash in copy.
func TestPasskeyTemplates(t *testing.T) {
	for _, name := range []string{"unlock", "enroll"} {
		tm, ok := Load()[name]
		if !ok {
			t.Fatalf("no %s template", name)
		}
		var b strings.Builder
		data := map[string]any{"Next": "/", "Recovery": true, "Error": "x"}
		if err := tm.Execute(&b, Page{Data: data, SignedIn: true, Nonce: "n0nce", CSRF: "c"}); err != nil {
			t.Fatal(err)
		}
		out := b.String()
		if strings.Contains(out, "—") {
			t.Errorf("%s: em dash", name)
		}
		if strings.Contains(out, "style=") || eventAttr.MatchString(out) {
			t.Errorf("%s: inline style or handler", name)
		}
		if !strings.Contains(out, `<script src="/static/passkey.js" nonce="n0nce" defer>`) {
			t.Errorf("%s: passkey.js not loaded with the nonce", name)
		}
	}
}

// The account page, with every optional block on, carries no inline style or
// handler, loads its script with the nonce and uses no em dash.
func TestAccountPageNoInlineStyles(t *testing.T) {
	tm, ok := Load()["account"]
	if !ok {
		t.Fatal("no account template")
	}
	data := map[string]any{
		"MB": "1.00 MB", "Usage": map[string]any{"Memories": 3, "Entities": 2, "Deleted": 1},
		"Keys": map[string]any{
			"Enrolled": true, "CanRemoveLast": true,
			"Passkeys":    []map[string]any{{"ID": "pk_1", "Name": "Laptop", "Created": "1 Jan 2026", "LastUsed": "2 Jan 2026"}, {"ID": "pk_2", "Name": "Phone", "Created": "3 Jan 2026"}},
			"NewRecovery": "AAAA-BBBB-CCCC-DDDD", "Error": "x", "Notice": "y", "Unlocked": true,
		},
	}
	var b strings.Builder
	if err := tm.Execute(&b, Page{Data: data, SignedIn: true, Nonce: "n0nce", CSRF: "c"}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if strings.Contains(out, "style=") || eventAttr.MatchString(out) || strings.Contains(out, "<style") {
		t.Error("inline style or handler")
	}
	if strings.Contains(out, "—") {
		t.Error("em dash")
	}
	if !strings.Contains(out, `<script src="/static/passkey.js" nonce="n0nce" defer>`) {
		t.Error("passkey.js not loaded with the nonce")
	}
	if !strings.Contains(out, `id="new-recovery"`) || !strings.Contains(out, "Forget all devices") {
		t.Error("recovery or forget block missing")
	}
}

// No em dash (U+2014) in any template, in README.md, or in an error string
// literal in the Go sources.
func TestNoEmDashInCopy(t *testing.T) {
	const dash = "—"
	ents, _ := files.ReadDir(".")
	for _, e := range ents {
		b, _ := files.ReadFile(e.Name())
		if strings.Contains(string(b), dash) {
			t.Errorf("%s: em dash", e.Name())
		}
	}
	root := "../../.."
	// README.md predates the rule, so only the lines this branch adds count.
	if base, err := exec.Command("git", "-C", root, "merge-base", "master", "HEAD").Output(); err == nil {
		out, err := exec.Command("git", "-C", root, "diff", "-U0", strings.TrimSpace(string(base)), "--", "README.md").Output()
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++") && strings.Contains(l, dash) {
				t.Errorf("README.md: em dash in added line: %s", l)
			}
		}
	}
	fset := token.NewFileSet()
	_ = filepath.WalkDir(filepath.Join(root), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if n := d.Name(); n == ".git" || n == "docs" || n == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); !ok || !((x.Name == "errors" && sel.Sel.Name == "New") || (x.Name == "fmt" && sel.Sel.Name == "Errorf")) {
				return true
			}
			for _, a := range call.Args {
				if lit, ok := a.(*ast.BasicLit); ok && strings.Contains(lit.Value, dash) {
					t.Errorf("%s: em dash in error literal %s", fset.Position(lit.Pos()), lit.Value)
				}
			}
			return true
		})
		return nil
	})
}
