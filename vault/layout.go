// Package vault projects a cortex knowledge graph onto a browsable
// markdown vault (Obsidian-compatible). It owns rendering and an
// on-disk manifest that tracks which entity rendered to which file.
//
// vault is a one-way projection: cortex remains the source of truth.
// Edits to vault files are never read back.
package vault

import (
	"strings"
	"unicode"
)

// slug normalizes a human name into a filename-safe slug.
// Rules: lowercase, runs of non-alphanumeric → single '-', trim '-'.
// Returns empty string if the input has no alphanumeric content;
// callers should fall back to entity ID in that case.
func slug(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(s))
	prevDash := true // suppress leading '-'
	for _, r := range s {
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

// typeFolderMap is the canonical pluralization for built-in entity types.
// Keys are lowercased.
var typeFolderMap = map[string]string{
	"person":       "people",
	"organization": "organizations",
	"concept":      "concepts",
	"event":        "events",
	"document":     "documents",
}

// folderForType returns the vault subdirectory name for an entity type.
// Unknown types fall through to lowercased(type) + "s".
func folderForType(t string) string {
	lc := strings.ToLower(t)
	if f, ok := typeFolderMap[lc]; ok {
		return f
	}
	return lc + "s"
}

// pageFilename builds the markdown filename for an entity.
// If colliding is true, the short ID suffix is always appended; this
// is how the exporter keeps colliding names stable across runs
// (every page in a collision set gets a suffix, never just some).
//
// The two ID helpers are intentionally asymmetric: the collision
// suffix slices from the tail of the ID (where ULID randomness lives),
// while the empty-slug fallback slices from the head (keeping the type
// prefix because there is no slug to provide type context).
func pageFilename(name, id string, colliding bool) string {
	base := slug(name)
	if base == "" {
		// Fall back to first 8 chars of ID (lowercased).
		base = shortID(id, 8)
		return base + ".md"
	}
	if colliding {
		return base + "-" + shortIDTail(id, 6) + ".md"
	}
	return base + ".md"
}

// shortID returns the first n chars of the ID, lowercased. Used as the
// empty-slug fallback in pageFilename: the type prefix is kept because
// there is no slug to provide type context.
func shortID(id string, n int) string {
	id = strings.ToLower(id)
	if len(id) <= n {
		return id
	}
	return id[:n]
}

// shortIDTail returns the last n chars of the ID, lowercased, with any
// "xxx_" type prefix already implicitly excluded (we slice from the end).
// Used for collision disambiguators because cortex IDs are ULIDs: the
// last 16 chars are pure randomness, while the first ~10 after the prefix
// are a millisecond timestamp shared by entities created in the same tick.
// Taking from the tail gives strong entropy even when entities collide
// on name AND creation time.
func shortIDTail(id string, n int) string {
	id = strings.ToLower(id)
	if len(id) <= n {
		return id
	}
	return id[len(id)-n:]
}
