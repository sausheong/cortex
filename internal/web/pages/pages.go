// Package pages holds the HTML templates of the web UI.
package pages

import (
	"embed"
	"html/template"
	"net/url"
	"strconv"
	"strings"
)

//go:embed *.tmpl
var files embed.FS

// Template is a parsed page (layout plus content).
type Template = template.Template

// Page is the data every template receives.
type Page struct {
	CSRF     string
	Nonce    string // per-response CSP nonce for inline scripts
	SignedIn bool
	Path     string // request path, used to mark the current nav item
	Data     any
	// Setup is the first-run checklist; nil once everything is done.
	Setup *Setup
	// SetUp is true once the whole checklist is done.
	SetUp bool
}

// Setup is the first-run checklist, ticked from real state.
type Setup struct {
	Provider, Connect, Memory bool
	// ShowPasskey adds step 1 (protect your memory with a passkey) for an
	// account that must enroll; Passkey is its tick. Gated is true while it
	// is undone, and the later steps then offer no way in.
	ShowPasskey, Passkey, Gated bool
	Done, Total                 int
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

// EscapePath percent-encodes each segment of a slash-separated path so names
// containing ?, #, % or spaces survive inside an href.
func EscapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// FmtInt groups thousands: 1274 -> "1,274".
func FmtInt(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// fmtAny groups thousands for any integer value; nil renders as 0.
func fmtAny(v any) string {
	switch x := v.(type) {
	case int:
		return FmtInt(x)
	case int64:
		return FmtInt(int(x))
	}
	return "0"
}

var funcs = template.FuncMap{"vpath": EscapePath, "n": fmtAny}

// Load parses every page template together with the layout, keyed by page
// name ("signin", "home", "settings", "notice").
func Load() map[string]*Template {
	out := map[string]*Template{}
	for _, n := range []string{"signin", "enroll", "unlock", "home", "settings", "notice", "consent", "connections", "memories", "graph", "deleted", "account", "compaction", "compaction_start", "confirm", "vault", "vault_page", "vault_missing"} {
		out[n] = template.Must(template.New("layout.tmpl").Funcs(funcs).ParseFS(files, "layout.tmpl", n+".tmpl"))
	}
	return out
}
