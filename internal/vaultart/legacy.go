package vaultart

import (
	"strings"
	"unicode"

	"github.com/sausheong/cortex/internal/workset"
)

// ResolveLegacy maps an old Vault markdown path (from the per-entity
// projection: "<folder>/<slug>.md" or "<folder>/<slug>-<shortid>.md") to the
// article that replaced it. It matches by the short id at the end of the
// file name, or by the name slug, against the entity's old folder, and only
// answers when the entity (or the topic it belongs to) currently has an
// article. More than one match is ambiguous and answers false. index.md and
// log.md map to Vault home. The path is only ever compared with strings
// computed from the caller's own data.
func ResolveLegacy(path string, ix *Index, set *workset.Set) (string, bool) {
	switch path {
	case "index.md", "log.md":
		return "/vault", true
	}
	if ix == nil || set == nil || !strings.HasSuffix(path, ".md") {
		return "", false
	}
	folder, file, ok := strings.Cut(strings.TrimSuffix(path, ".md"), "/")
	if !ok || folder == "" || file == "" || strings.Contains(file, "/") {
		return "", false
	}
	want := foldFolder(folder)
	found := map[string]bool{}
	var hit string
	for _, e := range set.Entities() {
		if foldFolder(legacyFolder(e.Type)) != want {
			continue
		}
		if !legacyFileMatches(file, e.Name, e.ID) {
			continue
		}
		p, ok := ix.PathFor(e.ID)
		if !ok || found[p] {
			continue
		}
		found[p] = true
		hit = p
	}
	if len(found) != 1 {
		return "", false
	}
	return hit, true
}

// legacyFileMatches reports whether the old file base name could have been
// written for an entity: its name slug, or its name slug plus "-" and the
// last six characters of its id (the exporter's collision form).
func legacyFileMatches(file, name, id string) bool {
	sl := legacySlug(name)
	if sl == "" {
		return false
	}
	if file == sl || file == Slug(name) {
		return true
	}
	tail := strings.ToLower(id)
	if len(tail) > 6 {
		tail = tail[len(tail)-6:]
	}
	return file == sl+"-"+tail || file == Slug(name)+"-"+tail
}

// legacySlug is the old exporter's file-name slug: lowercase letters and
// digits (Unicode kept), runs of anything else a single hyphen.
func legacySlug(s string) string {
	var b strings.Builder
	prevDash := true
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

var legacyFolders = map[string]string{
	"person": "people", "organization": "organizations", "concept": "concepts",
	"event": "events", "document": "documents",
}

// legacyFolder is the old projection's folder for an entity type.
func legacyFolder(t string) string {
	lc := strings.ToLower(t)
	if f, ok := legacyFolders[lc]; ok {
		return f
	}
	if sl := legacySlug(lc); sl != "" {
		return sl + "s"
	}
	return "others"
}

// foldFolder ignores spaces and hyphens so the old "email addresss" folders
// compare equal to their slugged form.
func foldFolder(f string) string {
	return strings.NewReplacer(" ", "", "-", "", "_", "").Replace(f)
}
