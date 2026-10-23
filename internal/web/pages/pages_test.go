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
