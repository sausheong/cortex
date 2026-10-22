// Package pages holds the HTML templates of the web UI.
package pages

import (
	"embed"
	"html/template"
)

//go:embed *.tmpl
var files embed.FS

// Template is a parsed page (layout plus content).
type Template = template.Template

// Page is the data every template receives.
type Page struct {
	CSRF     string
	SignedIn bool
	Data     any
}

// Load parses every page template together with the layout, keyed by page
// name ("signin", "settings", "notice").
func Load() map[string]*Template {
	out := map[string]*Template{}
	for _, n := range []string{"signin", "settings", "notice", "consent", "connections", "memories", "deleted", "account"} {
		out[n] = template.Must(template.ParseFS(files, "layout.tmpl", n+".tmpl"))
	}
	return out
}
