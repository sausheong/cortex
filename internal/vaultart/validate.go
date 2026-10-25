package vaultart

import (
	"errors"
	"regexp"
	"strings"

	"github.com/sausheong/cortex/internal/model"
)

// Length caps applied to model output.
const (
	maxTitleChars   = 120
	maxHeadingChars = 120
	maxItemChars    = 1200
)

// ErrNoContent is returned by Validate when nothing valid remains.
var ErrNoContent = errors.New("vaultart: article has no valid content")

// LinkTarget is an article a name can link to.
type LinkTarget struct {
	Group Group
	Slug  string
	Name  string
}

// Rendered is a validated, render-ready article. Text fields are plain text,
// not HTML: templates must escape them. Footnotes lists memory ids ordered by
// first appearance (lead first, then sections in order); every marker is a
// 1-based index into it, one number per memory id across the whole article.
// Link targets hold names; the web layer re-resolves them against the current
// index at render time.
type Rendered struct {
	Title       string
	Lead        string
	LeadMarkers []int
	Sections    []RenderedSection
	Related     []LinkTarget
	Footnotes   []string
}

// RenderedSection is a headed group of items.
type RenderedSection struct {
	Heading string
	Items   []RenderedItem
}

// RenderedItem is one paragraph or bullet with its source markers and links.
type RenderedItem struct {
	Text    string
	Bullet  bool
	Markers []int
	Links   []LinkTarget
}

// Option adjusts Validate.
type Option func(*options)

type options struct{ topic bool }

// ForTopic marks the article as a topic, the only kind whose model-written
// Title is kept. Without it the Title is dropped.
func ForTopic() Option { return func(o *options) { o.topic = true } }

var (
	emDash = regexp.MustCompile(`\s*[\x{2014}\x{2015}]\s*`)
	enDash = regexp.MustCompile(`\s*\x{2013}\s*`)
	numRng = regexp.MustCompile(`(\d)\s*\x{2013}\s*(\d)`)
)

// stripDashes replaces em dashes with a comma and en dashes with a hyphen
// between numbers or a comma elsewhere.
func stripDashes(s string) string {
	s = numRng.ReplaceAllString(s, "$1-$2")
	s = emDash.ReplaceAllString(s, ", ")
	s = enDash.ReplaceAllString(s, ", ")
	return s
}

func clean(s string, max int) string {
	s = strings.TrimSpace(stripDashes(s))
	if r := []rune(s); len(r) > max {
		s = strings.TrimSpace(string(r[:max]))
	}
	return s
}

// Validate checks a model-written article against the subject's own data and
// returns what is safe to render.
//
// Known limit: validation checks only that every cited id belongs to the
// subject (validMemoryIDs, which must hold the ids of this subject's memories
// and no others). It cannot verify that the text is supported by the cited
// memory, so a wrong or injected claim that cites a real memory still renders.
// The numbered sources let the owner check each claim.
//
// Rules: unknown source ids are dropped; a lead without a valid source is
// dropped on its own so no unsourced claim renders; items and sections left
// without valid sources are dropped; dashes are stripped; lengths are capped;
// links resolve case-insensitively by name or alias through linkable and
// unresolved ones are dropped (the name stays in the text as plain text).
// ErrNoContent is returned when nothing valid remains.
func Validate(raw model.Article, validMemoryIDs map[string]bool, linkable map[string]LinkTarget, opts ...Option) (Rendered, error) {
	var o options
	for _, f := range opts {
		f(&o)
	}
	norm := make(map[string]LinkTarget, len(linkable))
	for k, v := range linkable {
		norm[strings.ToLower(strings.TrimSpace(k))] = v
	}
	resolve := func(names []string) []LinkTarget {
		var out []LinkTarget
		seen := map[string]bool{}
		for _, n := range names {
			t, ok := norm[strings.ToLower(strings.TrimSpace(n))]
			if !ok {
				continue
			}
			k := string(t.Group) + "|" + t.Slug
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, t)
		}
		return out
	}
	validIDs := func(ids []string) []string {
		var out []string
		seen := map[string]bool{}
		for _, id := range ids {
			if validMemoryIDs[id] && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
		return out
	}

	type item struct {
		text    string
		bullet  bool
		sources []string
		links   []LinkTarget
	}
	type section struct {
		heading string
		items   []item
	}
	var secs []section
	for _, s := range raw.Sections {
		heading := clean(s.Heading, maxHeadingChars)
		if heading == "" {
			continue
		}
		var items []item
		for _, it := range s.Items {
			text := clean(it.Text, maxItemChars)
			src := validIDs(it.Sources)
			if text == "" || len(src) == 0 {
				continue
			}
			items = append(items, item{text, it.Bullet, src, resolve(it.Links)})
		}
		if len(items) > 0 {
			secs = append(secs, section{heading, items})
		}
	}

	lead := clean(raw.Lead, maxItemChars)
	leadSrc := validIDs(raw.LeadSources)
	if lead == "" || len(leadSrc) == 0 {
		lead, leadSrc = "", nil
	}
	if lead == "" && len(secs) == 0 {
		return Rendered{}, ErrNoContent
	}

	var r Rendered
	if o.topic {
		r.Title = clean(raw.Title, maxTitleChars)
	}
	nums := map[string]int{}
	mark := func(ids []string) []int {
		out := make([]int, 0, len(ids))
		for _, id := range ids {
			n, ok := nums[id]
			if !ok {
				r.Footnotes = append(r.Footnotes, id)
				n = len(r.Footnotes)
				nums[id] = n
			}
			out = append(out, n)
		}
		return out
	}
	r.Lead = lead
	if lead != "" {
		r.LeadMarkers = mark(leadSrc)
	}
	for _, s := range secs {
		rs := RenderedSection{Heading: s.heading}
		for _, it := range s.items {
			rs.Items = append(rs.Items, RenderedItem{Text: it.text, Bullet: it.bullet, Markers: mark(it.sources), Links: it.links})
		}
		r.Sections = append(r.Sections, rs)
	}
	r.Related = resolve(raw.Related)
	return r, nil
}
