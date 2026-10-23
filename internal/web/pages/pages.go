// Package pages holds the HTML templates of the web UI.
package pages

import (
	"embed"
	"html/template"
	"strings"
)

//go:embed *.tmpl
var files embed.FS

// Template is a parsed page (layout plus content).
type Template = template.Template

// Page is the data every template receives.
type Page struct {
	CSRF     string
	SignedIn bool
	Path     string // request path, used to mark the current nav item
	Data     any
	// Setup is the first-run checklist; nil once everything is done.
	Setup *Setup
}

// Setup is the first-run checklist, ticked from real state.
type Setup struct {
	Provider, Connect, Memory bool
	Done                      int
}

// Current returns aria-current="page" markup when href is the top-level
// nav section the request path belongs to. Compaction and Recently deleted
// are part of Memories.
func (p Page) Current(href string) template.HTMLAttr {
	on := within(p.Path, href)
	if href == "/memories" {
		on = on || within(p.Path, "/compaction")
	}
	if on {
		return `aria-current="page"`
	}
	return ""
}

// CurrentSub marks the item of the Memories sub-navigation the request path
// belongs to. /memories/deleted is its own item, not "All memories".
func (p Page) CurrentSub(href string) template.HTMLAttr {
	on := within(p.Path, href)
	if href == "/memories" && within(p.Path, "/memories/deleted") {
		on = false
	}
	if on {
		return `aria-current="page"`
	}
	return ""
}

func within(path, href string) bool {
	return path == href || strings.HasPrefix(path, href+"/")
}

// Load parses every page template together with the layout, keyed by page
// name ("signin", "settings", "notice").
func Load() map[string]*Template {
	out := map[string]*Template{}
	for _, n := range []string{"signin", "settings", "notice", "consent", "connections", "memories", "graph", "deleted", "account", "compaction", "compaction_start", "confirm"} {
		out[n] = template.Must(template.ParseFS(files, "layout.tmpl", n+".tmpl"))
	}
	return out
}
