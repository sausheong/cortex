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
}

// Current returns aria-current="page" markup when href is the section the
// request path belongs to. /memories/deleted is its own item, not Memories.
func (p Page) Current(href string) template.HTMLAttr {
	path := p.Path
	on := path == href
	if !on && href != "/memories/deleted" {
		on = strings.HasPrefix(path, href+"/") && !(href == "/memories" && strings.HasPrefix(path, "/memories/deleted"))
	}
	if on {
		return `aria-current="page"`
	}
	return ""
}

// Load parses every page template together with the layout, keyed by page
// name ("signin", "settings", "notice").
func Load() map[string]*Template {
	out := map[string]*Template{}
	for _, n := range []string{"signin", "settings", "notice", "consent", "connections", "memories", "deleted", "account", "compaction", "compaction_start", "confirm"} {
		out[n] = template.Must(template.ParseFS(files, "layout.tmpl", n+".tmpl"))
	}
	return out
}
