package web

import (
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sausheong/cortex/internal/store"
	"github.com/sausheong/cortex/internal/vaultart"
)

// Fixed sentences for the article states. None carries user or model text.
const (
	msgRewriteLimit = "You have rewritten several articles this hour. Try again in a little while."
	msgBadArticle   = "This article could not be shown. Rewrite it to try again."
	msgCouldntPre   = "We couldn't update this article just now, so this is the previous version. "
	sourcePreview   = 140

	msgCorrectLimit = "You have saved many corrections this hour. Try again in a little while."
	msgBadCorrect   = "Write a correction of up to 2,000 characters, then save it."
	msgNoCorrection = "That correction could not be found on this article."
	flashSaved      = "Saved. This article will be rewritten the next time you open it."
	flashUndone     = "Correction removed. This article will be rewritten the next time you open it."
	noteWaiting     = "A correction is waiting to be included."
)

// flashFor maps the note query value to its fixed sentence. Nothing from the
// request is ever echoed.
func flashFor(v string) string {
	switch v {
	case "saved":
		return flashSaved
	case "undone":
		return flashUndone
	}
	return ""
}

type artCorrection struct{ ID, Text, Ago, Abs, ISO string }

func groupOf(v string) (vaultart.Group, bool) {
	switch g := vaultart.Group(v); g {
	case vaultart.GroupPeople, vaultart.GroupProjects, vaultart.GroupTopics:
		return g, true
	}
	return "", false
}

// vaultSubject resolves the request's group and slug to a subject of the
// signed-in user's index. ok is false when the page does not exist (the
// caller answers 404); redirect is the canonical path when the slug's name
// part is out of date.
func vaultSubject(vr *vaultReq, r *http.Request) (sub vaultart.Subject, canonical string, ok bool) {
	g, gok := groupOf(r.PathValue("group"))
	if !gok {
		return vaultart.Subject{}, "", false
	}
	sub, slug, ok := vr.ix.Lookup(g, r.PathValue("slug"))
	if !ok {
		return vaultart.Subject{}, "", false
	}
	return sub, vaultart.Path(g, slug), true
}

type artSeg struct{ Text, Href string }

type artMark struct {
	N    int
	Href string
}

type artItem struct {
	Segs  []artSeg
	Marks []artMark
}

type artBlock struct {
	Bullets bool
	Items   []artItem
}

type artSection struct {
	Heading string
	Blocks  []artBlock
}

type artSource struct {
	N    int
	Href string
	Text string
	Ago  string
	Abs  string
	ISO  string
}

type artRel struct{ Path, Name, Descriptor string }

type articleView struct {
	Title, Descriptor string
	Lead              artItem
	HasLead           bool
	Sections          []artSection
	Sources           []artSource
	Related           []artRel
	Also              []string
	Memories          int
	Ago, Abs, ISO     string
	Path              string
	Note              string
	GraphHref         string
	MemoriesHref      string
	Flash             string
	Waiting           bool
	Corrections       []artCorrection
}

type vaultWritingView struct {
	Busy  bool
	Flash string
}

type vaultErrorView struct {
	Message string
	// Action is the POST that retries: "retry", "rewrite" or "" for none.
	Action     string
	Path       string
	Settings   bool // offer a link to Settings
	BackToPage bool // offer a link back to the article
}

func memHref(id string) string { return "/memories?id=" + url.QueryEscape(id) }

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// findFold finds name in text ignoring case, at word boundaries, and returns
// its byte range or -1.
func findFold(text, name string) (int, int) {
	n := utf8.RuneCountInString(name)
	if n == 0 {
		return -1, -1
	}
	for i := range text {
		j, c := i, 0
		for c < n && j < len(text) {
			_, sz := utf8.DecodeRuneInString(text[j:])
			j += sz
			c++
		}
		if c < n {
			break
		}
		if !strings.EqualFold(text[i:j], name) {
			continue
		}
		if p, _ := utf8.DecodeLastRuneInString(text[:i]); i > 0 && isWordRune(p) {
			continue
		}
		if q, _ := utf8.DecodeRuneInString(text[j:]); j < len(text) && isWordRune(q) {
			continue
		}
		return i, j
	}
	return -1, -1
}

// linkSegments splits prose into plain and linked runs. A link is placed at
// the first whole-word mention of the target's name or alias; a target the
// text does not mention is simply not linked. The text is never HTML: the
// template escapes every run.
func linkSegments(text string, targets []vaultart.LinkTarget, links map[string]vaultart.LinkTarget) []artSeg {
	type hit struct {
		a, b int
		href string
	}
	var hits []hit
	for _, t := range targets {
		for _, name := range vaultart.NamesFor(links, t) {
			a, b := findFold(text, name)
			if a < 0 {
				continue
			}
			hits = append(hits, hit{a, b, vaultart.Path(t.Group, t.Slug)})
			break
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].a < hits[j].a })
	var out []artSeg
	pos := 0
	for _, h := range hits {
		if h.a < pos {
			continue // overlaps an earlier link
		}
		if h.a > pos {
			out = append(out, artSeg{Text: text[pos:h.a]})
		}
		out = append(out, artSeg{Text: text[h.a:h.b], Href: h.href})
		pos = h.b
	}
	if pos < len(text) {
		out = append(out, artSeg{Text: text[pos:]})
	}
	if len(out) == 0 {
		out = []artSeg{{Text: text}}
	}
	return out
}

func marks(ns []int) []artMark {
	out := make([]artMark, len(ns))
	for i, n := range ns {
		out[i] = artMark{N: n}
	}
	return out
}

// buildArticle renders a stored article for display. The stored text is
// validated again against the subject's current memories and the current
// index, so a source that no longer exists is dropped and a name that no
// longer qualifies renders as plain text.
func (s *server) buildArticle(vr *vaultReq, sub vaultart.Subject, row *store.ArticleRow) (articleView, error) {
	entry, _ := vr.ix.Entry(sub.ID)
	valid := vaultart.MemoryIDs(vr.set, sub)
	links := vr.ix.Linkables(sub)
	var opts []vaultart.Option
	if sub.Kind == "topic" {
		opts = append(opts, vaultart.ForTopic())
	}
	rd, err := vaultart.Validate(row.Article, valid, links, opts...)
	if err != nil {
		return articleView{}, err
	}
	now := s.now()
	v := articleView{Title: entry.Name, Descriptor: entry.Descriptor, Path: entry.Path, Memories: len(valid)}
	if sub.Kind == "entity" && vr.ownerID != "" && sub.ID == vr.ownerID {
		v.Title, v.Descriptor = "You", ""
	}
	if v.Descriptor == v.Title {
		v.Descriptor = ""
	}
	mk := func(text string, ms []int, ls []vaultart.LinkTarget) artItem {
		it := artItem{Segs: linkSegments(text, ls, links), Marks: marks(ms)}
		for i := range it.Marks {
			it.Marks[i].Href = memHref(rd.Footnotes[it.Marks[i].N-1])
		}
		return it
	}
	if rd.Lead != "" {
		v.HasLead = true
		v.Lead = mk(rd.Lead, rd.LeadMarkers, nil)
	}
	for _, sec := range rd.Sections {
		as := artSection{Heading: sec.Heading}
		for _, it := range sec.Items {
			ai := mk(it.Text, it.Markers, it.Links)
			if n := len(as.Blocks); n > 0 && as.Blocks[n-1].Bullets == it.Bullet {
				as.Blocks[n-1].Items = append(as.Blocks[n-1].Items, ai)
			} else {
				as.Blocks = append(as.Blocks, artBlock{Bullets: it.Bullet, Items: []artItem{ai}})
			}
		}
		v.Sections = append(v.Sections, as)
	}
	for i, id := range rd.Footnotes {
		src := artSource{N: i + 1, Href: memHref(id)}
		if m, ok := vr.set.MemoryByID(id); ok {
			src.Text = preview(m.Content)
			src.Ago, src.Abs, src.ISO = relTime(now, m.CreatedAt), absTime(m.CreatedAt), m.CreatedAt.UTC().Format(time.RFC3339)
		}
		v.Sources = append(v.Sources, src)
	}
	for _, e := range vr.ix.Related(sub, 8) {
		v.Related = append(v.Related, artRel{Path: e.Path, Name: e.Name, Descriptor: e.Descriptor})
	}
	v.Also = vr.ix.AlsoMentioned(sub)
	v.Ago, v.Abs, v.ISO = relTime(now, row.WrittenAt), absTime(row.WrittenAt), row.WrittenAt.UTC().Format(time.RFC3339)
	for _, c := range vaultart.ListCorrections(vr.set, sub) {
		v.Corrections = append(v.Corrections, artCorrection{ID: c.ID, Text: c.Text,
			Ago: relTime(now, c.At), Abs: absTime(c.At), ISO: c.At.UTC().Format(time.RFC3339)})
	}
	v.Waiting = row.Stale && len(v.Corrections) > 0
	if sub.Kind == "entity" {
		v.GraphHref = "/graph?center=" + url.QueryEscape(sub.ID)
		v.MemoriesHref = "/memories?entity=" + url.QueryEscape(sub.ID)
	}
	return v, nil
}

// preview is a one-line, length-capped excerpt of a memory (user text, shown
// as written).
func preview(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > sourcePreview {
		return strings.TrimSpace(string(r[:sourcePreview])) + "…"
	}
	return s
}

// vaultArticle shows one article. It never calls the model on the request:
// a missing or out-of-date article starts a background write and answers a
// calm "Writing" page that refreshes itself.
func (s *server) vaultArticle(w http.ResponseWriter, r *http.Request, u SessionUser) {
	if _, ok := groupOf(r.PathValue("group")); !ok || strings.HasSuffix(r.PathValue("slug"), ".md") {
		// An old markdown URL such as /vault/people/ada.md.
		s.legacy(w, r, u, r.PathValue("group")+"/"+r.PathValue("slug"))
		return
	}
	vr, ok := s.vaultLoad(w, r, u)
	if !ok {
		return
	}
	defer vr.release()
	sub, canon, ok := vaultSubject(vr, r)
	if !ok {
		s.vaultMissing(w, r)
		return
	}
	if r.URL.Path != canon {
		s.vaultRedirect(w, r, canon, http.StatusFound)
		return
	}
	res := s.art.Open(r.Context(), vaultart.User{ID: u.UserID, Key: u.Key, Cx: vr.cx}, sub, false)
	msg := vaultart.MsgGeneric
	if res.Err != nil {
		msg = res.Err.Error()
	}
	switch res.State {
	case vaultart.Locked:
		s.vaultRedirect(w, r, "/unlock?next="+url.QueryEscape(safeNext(r.URL.RequestURI())), http.StatusSeeOther)
	case vaultart.Writing, vaultart.Busy:
		s.render(w, r, http.StatusOK, "vault_writing", vaultWritingView{Busy: res.State == vaultart.Busy, Flash: flashFor(r.URL.Query().Get("note"))})
	case vaultart.RateLimited:
		s.render(w, r, http.StatusTooManyRequests, "vault_error", vaultErrorView{Message: msg, Path: canon, BackToPage: false})
	case vaultart.Failed:
		ev := vaultErrorView{Message: msg, Action: "retry", Path: canon}
		if msg == vaultart.MsgCannotWrite {
			ev.Action, ev.Settings = "", true
		}
		s.render(w, r, http.StatusOK, "vault_error", ev)
	default: // Ready
		if res.Row == nil {
			s.render(w, r, http.StatusOK, "vault_error", vaultErrorView{Message: vaultart.MsgGeneric, Action: "retry", Path: canon})
			return
		}
		v, err := s.buildArticle(vr, sub, res.Row)
		if err != nil {
			if !errors.Is(err, vaultart.ErrNoContent) {
				s.cfg.Logger.Error("vault article render")
			}
			s.render(w, r, http.StatusOK, "vault_error", vaultErrorView{Message: msgBadArticle, Action: "rewrite", Path: canon})
			return
		}
		if res.CouldntUpdate {
			v.Note = msgCouldntPre + msg
		}
		v.Flash = flashFor(r.URL.Query().Get("note"))
		s.render(w, r, http.StatusOK, "vault_article", v)
	}
}

// vaultRewrite forces a fresh write, within the per-user hourly limit.
func (s *server) vaultRewrite(w http.ResponseWriter, r *http.Request, u SessionUser) {
	vr, ok := s.vaultLoad(w, r, u)
	if !ok {
		return
	}
	defer vr.release()
	sub, canon, ok := vaultSubject(vr, r)
	if !ok {
		s.vaultMissing(w, r)
		return
	}
	if !s.art.AllowRewrite(u.UserID) {
		s.render(w, r, http.StatusTooManyRequests, "vault_error", vaultErrorView{Message: msgRewriteLimit, Path: canon, BackToPage: true})
		return
	}
	s.art.Open(r.Context(), vaultart.User{ID: u.UserID, Key: u.Key, Cx: vr.cx}, sub, true)
	s.vaultRedirect(w, r, canon, http.StatusSeeOther)
}

// vaultRetry forgets a remembered failure so the next open starts afresh.
func (s *server) vaultRetry(w http.ResponseWriter, r *http.Request, u SessionUser) {
	vr, ok := s.vaultLoad(w, r, u)
	if !ok {
		return
	}
	defer vr.release()
	sub, canon, ok := vaultSubject(vr, r)
	if !ok {
		s.vaultMissing(w, r)
		return
	}
	s.art.ClearFailure(u.UserID, sub.ID)
	s.vaultRedirect(w, r, canon, http.StatusSeeOther)
}

// vaultCorrect saves a correction as a memory about the subject, within the
// per-user hourly limit, and sends the reader back to the article.
func (s *server) vaultCorrect(w http.ResponseWriter, r *http.Request, u SessionUser) {
	vr, ok := s.vaultLoad(w, r, u)
	if !ok {
		return
	}
	defer vr.release()
	sub, canon, ok := vaultSubject(vr, r)
	if !ok {
		s.vaultMissing(w, r)
		return
	}
	if !s.art.AllowCorrect(u.UserID) {
		s.render(w, r, http.StatusTooManyRequests, "vault_error", vaultErrorView{Message: msgCorrectLimit, Path: canon, BackToPage: true})
		return
	}
	_, err := vaultart.AddCorrection(r.Context(), vr.cx, sub, r.PostFormValue("text"))
	switch {
	case errors.Is(err, vaultart.ErrBadCorrection):
		s.render(w, r, http.StatusBadRequest, "vault_error", vaultErrorView{Message: msgBadCorrect, Path: canon, BackToPage: true})
	case err != nil:
		s.cfg.Logger.Error("vault correct")
		http.Error(w, "internal error", http.StatusInternalServerError)
	default:
		s.vaultRedirect(w, r, canon+"?note=saved", http.StatusSeeOther)
	}
}

// vaultUndo removes one of the reader's own corrections of the subject.
func (s *server) vaultUndo(w http.ResponseWriter, r *http.Request, u SessionUser) {
	vr, ok := s.vaultLoad(w, r, u)
	if !ok {
		return
	}
	defer vr.release()
	sub, canon, ok := vaultSubject(vr, r)
	if !ok {
		s.vaultMissing(w, r)
		return
	}
	err := vaultart.UndoCorrection(r.Context(), vr.cx, sub, r.PostFormValue("memory"))
	switch {
	case errors.Is(err, vaultart.ErrNotCorrection):
		s.render(w, r, http.StatusNotFound, "vault_error", vaultErrorView{Message: msgNoCorrection, Path: canon, BackToPage: true})
	case err != nil:
		s.cfg.Logger.Error("vault undo")
		http.Error(w, "internal error", http.StatusInternalServerError)
	default:
		s.vaultRedirect(w, r, canon+"?note=undone", http.StatusSeeOther)
	}
}
