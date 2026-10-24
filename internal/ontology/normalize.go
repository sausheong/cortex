package ontology

import (
	"regexp"
	"strings"
)

// typeSepRe matches the runs of whitespace, hyphens and underscores that
// NormalizeType collapses into a single underscore.
var typeSepRe = regexp.MustCompile(`[\s\-_]+`)

// NormalizeType canonicalises an entity or relationship type label:
// lower-cased, trimmed, with runs of spaces, hyphens and underscores
// collapsed to one underscore. "Person", " person " and "PERSON" all become
// "person"; "Works At" and "works-at" become "works_at". Without this the
// (name, type) uniqueness of entities splits one thing into several rows
// when an extractor varies its casing.
func NormalizeType(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	t = typeSepRe.ReplaceAllString(t, "_")
	return strings.Trim(t, "_")
}

// NormTypeSQL is the SQL equivalent of NormalizeType applied to col, for
// matching rows whose stored type predates normalisation.
func NormTypeSQL(col string) string {
	return `btrim(regexp_replace(lower(btrim(` + col + `)), '[[:space:]_-]+', '_', 'g'), '_')`
}

// NormKey lower-cases s and collapses whitespace; it is the key under which
// extracted names and types are compared.
func NormKey(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}
