package vaultart

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

const (
	maxSlugName = 48
	shortIDLen  = 8
)

var asciiExtra = strings.NewReplacer(
	"ß", "ss", "æ", "ae", "Æ", "ae", "œ", "oe", "Œ", "oe",
	"ø", "o", "Ø", "o", "ł", "l", "Ł", "l", "đ", "d", "Đ", "d", "ð", "d", "þ", "th",
)

// Slug folds a name to lowercase ASCII words joined by hyphens, at most 48
// characters. A name with nothing usable becomes "untitled".
func Slug(name string) string {
	var b strings.Builder
	pendingHyphen := false
	for _, r := range norm.NFD.String(asciiExtra.Replace(name)) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		r = unicode.ToLower(r)
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if pendingHyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			pendingHyphen = false
			b.WriteRune(r)
			continue
		}
		pendingHyphen = true
	}
	s := b.String()
	if len(s) > maxSlugName {
		s = strings.TrimRight(s[:maxSlugName], "-")
	}
	if s == "" {
		return "untitled"
	}
	return s
}

// ShortID is the lookup key of a subject: the last 8 characters of its
// random id, lowercased. The name part of a slug is decorative.
func ShortID(id string) string {
	id = strings.ToLower(id)
	if len(id) > shortIDLen {
		id = id[len(id)-shortIDLen:]
	}
	return id
}

// ArticleSlug is <name-slug>-<short-id>.
func ArticleSlug(name, id string) string { return Slug(name) + "-" + ShortID(id) }

// Path is the URL path of a Vault page.
func Path(group Group, slug string) string { return "/vault/" + string(group) + "/" + slug }

// ParseSlug extracts the short id from a slug. Only the short id is used
// for lookup; a wrong or stale name part still parses.
func ParseSlug(slug string) (shortID string, ok bool) {
	i := strings.LastIndexByte(slug, '-')
	if i <= 0 {
		return "", false
	}
	id := slug[i+1:]
	if len(id) != shortIDLen {
		return "", false
	}
	for _, r := range id {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return "", false
		}
	}
	return strings.ToLower(id), true
}

// ResolveShortID finds the one subject id with the given short id. found is
// false when none match; ambiguous is true when two or more do, so callers
// answer 404 rather than guess.
func ResolveShortID(ids []string, short string) (id string, found, ambiguous bool) {
	short = strings.ToLower(short)
	for _, c := range ids {
		if ShortID(c) != short {
			continue
		}
		if found && c != id {
			return "", false, true
		}
		id, found = c, true
	}
	return id, found, false
}
