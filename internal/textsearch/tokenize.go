// Package textsearch provides in-memory keyword scorers that stand in for
// Postgres full-text search once content is encrypted at rest. The tokenizer
// emulates to_tsvector('english', ...) closely enough that the memory scorer
// ranks identically to the legacy SQL path.
package textsearch

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/kljensen/snowball"
)

// maxIndexedRunes mirrors left(content, 200000) in the legacy generated
// tsvector columns.
const maxIndexedRunes = 200000

// stopwords is Postgres's english.stop.
var stopwords = func() map[string]struct{} {
	m := map[string]struct{}{}
	for _, w := range strings.Fields(`i me my myself we our ours ourselves you your yours yourself
yourselves he him his himself she her hers herself it its itself they them their theirs themselves
what which who whom this that these those am is are was were be been being have has had having do
does did doing a an the and but if or because as until while of at by for with about against between
into through during before after above below to from up down in out on off over under again further
then once here there when where why how all any both each few more most other some such no nor not
only own same so than too very s t can will just don should now`) {
		m[w] = struct{}{}
	}
	return m
}()

const (
	wc  = `[\p{L}\p{N}]+`
	dot = wc + `(?:\.` + wc + `)+`
)

// Alternatives are tried in order at each position (leftmost wins).
var tokRE = regexp.MustCompile(`(?i)` +
	// email
	`(?P<email>[\p{L}\p{N}_.+-]+@` + dot + `)` +
	// url: optional scheme, dotted host, optional path
	`|(?P<url>(?:(?:https?|ftp)://)?` + dot + `(?::\d+)?(?:/[^\s]*)?)` +
	// dotted token kept whole: hosts, floats, versions, e.g. u.s.a
	`|(?P<dotted>` + dot + `)` +
	// file path with at least two segments
	`|(?P<path>(?:/[\p{L}\p{N}_.~-]+){2,})` +
	// digit run followed by signed digit runs: 2024-01-05
	`|(?P<ints>\d+(?:-\d+)+)` +
	// hyphenated compound
	`|(?P<hyph>` + wc + `(?:-` + wc + `)+)` +
	`|(?P<word>` + wc + `)`)

var (
	idxURL, idxEmail, idxDotted, idxPath, idxInts, idxHyph, idxWord int
)

func init() {
	for i, n := range tokRE.SubexpNames() {
		switch n {
		case "url":
			idxURL = i
		case "email":
			idxEmail = i
		case "dotted":
			idxDotted = i
		case "path":
			idxPath = i
		case "ints":
			idxInts = i
		case "hyph":
			idxHyph = i
		case "word":
			idxWord = i
		}
	}
}

func stemWord(w string) string {
	if _, stop := stopwords[w]; stop {
		return ""
	}
	st, err := snowball.Stem(w, "english", true)
	if err != nil {
		return w
	}
	return st
}

// Lexemes returns the lexemes of text in order of appearance, with
// repetitions (term frequency is preserved for BM25). Callers needing a set
// dedupe themselves.
func Lexemes(text string) []string {
	if utf8.RuneCountInString(text) > maxIndexedRunes {
		text = string([]rune(text)[:maxIndexedRunes])
	}
	text = strings.ToLower(text)
	var out []string
	add := func(s string) {
		if s != "" {
			out = append(out, s)
		}
	}
	for _, m := range tokRE.FindAllStringSubmatchIndex(text, -1) {
		sub := func(i int) (string, bool) {
			if m[2*i] < 0 {
				return "", false
			}
			return text[m[2*i]:m[2*i+1]], true
		}
		switch {
		case has(m, idxURL):
			s, _ := sub(idxURL)
			emitURL(s, add)
		case has(m, idxEmail):
			s, _ := sub(idxEmail)
			add(s)
		case has(m, idxDotted):
			s, _ := sub(idxDotted)
			add(s)
		case has(m, idxPath):
			s, _ := sub(idxPath)
			add(s)
		case has(m, idxInts):
			s, _ := sub(idxInts)
			parts := strings.Split(s, "-")
			add(parts[0])
			for _, p := range parts[1:] {
				add("-" + p)
			}
		case has(m, idxHyph):
			s, _ := sub(idxHyph)
			add(stemWord(s))
			for _, p := range strings.Split(s, "-") {
				add(stemWord(p))
			}
		case has(m, idxWord):
			s, _ := sub(idxWord)
			add(stemWord(s))
		}
	}
	return out
}

func has(m []int, i int) bool { return m[2*i] >= 0 }

// emitURL splits a url token into host, and (when a path exists) the
// scheme-less url and the path, as the Postgres parser does.
func emitURL(s string, add func(string)) {
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	s = strings.TrimRight(s, ".,;:!?)'\"")
	host, path := s, ""
	if i := strings.Index(s, "/"); i >= 0 {
		host, path = s[:i], s[i:]
	}
	host = strings.TrimSuffix(host, ":") // defensive
	if j := strings.LastIndex(host, ":"); j >= 0 {
		host = host[:j] // drop port
	}
	if path != "" {
		add(s)
	}
	add(host)
	if path != "" {
		add(path)
	}
}

// QueryLexemes returns the deduplicated union of lexemes over the
// whitespace-separated tokens of q (OR semantics, like orTSQuery).
func QueryLexemes(q string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, tok := range queryTokens(q) {
		for _, l := range tok {
			if _, ok := seen[l]; !ok {
				seen[l] = struct{}{}
				out = append(out, l)
			}
		}
	}
	return out
}

// queryTokens mirrors plainto_tsquery per whitespace token: each token yields
// the distinct lexemes that must all be present (AND). Tokens that reduce to
// nothing (stopwords) are dropped.
func queryTokens(q string) [][]string {
	var out [][]string
	for _, f := range strings.Fields(q) {
		lex := distinct(Lexemes(f))
		if len(lex) > 0 {
			out = append(out, lex)
		}
	}
	return out
}

func distinct(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := in[:0:0]
	for _, s := range in {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}
