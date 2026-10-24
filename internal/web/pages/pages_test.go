package pages

import (
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
