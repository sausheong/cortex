package llmutil

import "strings"

// Truncate returns s if shorter than n, else s[:n] + "...".
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// TruncateRunes returns s cut to at most n runes.
func TruncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// EscapeLike escapes the LIKE wildcards in s for use with ESCAPE '\'.
func EscapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
