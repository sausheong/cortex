package web

import (
	"archive/zip"
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/vault"
)

const (
	vaultPerPage   = 100
	vaultCacheMax  = 8
	vaultCacheByte = 24 << 20 // approximate rendered bytes across all cached vaults
	vaultCacheTTL  = 10 * time.Minute
	vaultZipPerMin = 6
)

// vaultSet is one user's rendered vault plus the lookups browsing needs.
type vaultSet struct {
	set       *vault.Set
	byPath    map[string]int    // path -> index in set.Files
	byKey     map[string]string // lowercased path without .md -> path
	byBase    map[string]string // lowercased basename without .md -> path ("" if ambiguous)
	backlinks map[string][]backlink
	folders   []folderCount
	builtAt   time.Time
	bytes     int
}

type backlink struct{ Path, Name, Rel string }

type folderCount struct {
	Key, Label string
	Count      int
}

var folderLabels = map[string]string{
	"people": "People", "organizations": "Organisations", "concepts": "Projects",
	"events": "Events", "documents": "Documents", "sources": "Pages",
}

var folderOrder = []string{"people", "organizations", "concepts", "events", "documents"}

func folderLabel(k string) string {
	if l, ok := folderLabels[k]; ok {
		return l
	}
	if k == "" {
		return "Index"
	}
	return strings.ToUpper(k[:1]) + k[1:]
}

func newVaultSet(set *vault.Set, now time.Time) *vaultSet {
	v := &vaultSet{set: set, byPath: map[string]int{}, byKey: map[string]string{}, byBase: map[string]string{},
		backlinks: map[string][]backlink{}, builtAt: now}
	for _, f := range set.Files {
		v.bytes += 2*len(f.Content) + len(f.Path) + 256
	}
	counts := map[string]int{}
	for i, f := range set.Files {
		v.byPath[f.Path] = i
		key := strings.ToLower(strings.TrimSuffix(f.Path, ".md"))
		v.byKey[key] = f.Path
		base := key[strings.LastIndex(key, "/")+1:]
		if _, dup := v.byBase[base]; dup {
			v.byBase[base] = ""
		} else {
			v.byBase[base] = f.Path
		}
		if f.Kind != vault.KindIndex {
			counts[f.Folder]++
		}
	}
	var keys []string
	for k := range counts {
		keys = append(keys, k)
	}
	rank := func(k string) int {
		if k == "sources" {
			return 1000
		}
		for i, o := range folderOrder {
			if o == k {
				return i
			}
		}
		return 500
	}
	sort.Slice(keys, func(i, j int) bool {
		if ri, rj := rank(keys[i]), rank(keys[j]); ri != rj {
			return ri < rj
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		v.folders = append(v.folders, folderCount{k, folderLabel(k), counts[k]})
	}
	for _, f := range set.Files {
		seen := map[string]bool{}
		for _, t := range wikiTargets(f.Content) {
			p, ok := v.resolve(t)
			if !ok || p == f.Path || seen[p] {
				continue
			}
			seen[p] = true
			v.backlinks[p] = append(v.backlinks[p], backlink{Path: f.Path, Name: f.Name, Rel: relLabelIf(relContext(f.Content, t))})
		}
	}
	return v
}

// relContext finds the relationship word in the exporter's "- type → [[...]]"
// line that links to target, or "" when the link is not a relationship.
func relContext(content, target string) string {
	for _, ln := range strings.Split(content, "\n") {
		if !strings.HasPrefix(ln, "- ") || !strings.Contains(ln, "[["+target) {
			continue
		}
		if t, rest, ok := strings.Cut(strings.TrimPrefix(ln, "- "), " → "); ok && !strings.Contains(t, " ") && strings.HasPrefix(rest, "[[") {
			return t
		}
	}
	return ""
}

// resolve maps a wikilink target to a page of this vault.
func (v *vaultSet) resolve(target string) (string, bool) {
	t, _, _ := strings.Cut(target, "#")
	t = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(t), ".md")))
	if t == "" {
		return "", false
	}
	if p, ok := v.byKey[t]; ok {
		return p, true
	}
	if !strings.Contains(t, "/") {
		if p := v.byBase[t]; p != "" {
			return p, true
		}
	}
	return "", false
}

func (v *vaultSet) file(path string) (vault.File, bool) {
	i, ok := v.byPath[path]
	if !ok {
		return vault.File{}, false
	}
	return v.set.Files[i], true
}

// vaultCache holds each user's vault briefly, keyed by a cheap version of
// their data, so browsing does not re-render hundreds of pages per click.
type vaultCache struct {
	mu sync.Mutex
	m  map[string]*vaultEntry
}

type vaultEntry struct {
	mu      sync.Mutex
	version string
	v       *vaultSet
}

func (c *vaultCache) entry(user string) *vaultEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]*vaultEntry{}
	}
	e, ok := c.m[user]
	if !ok {
		if len(c.m) >= vaultCacheMax {
			var oldest string
			var ot time.Time
			for k, o := range c.m {
				if o.v == nil {
					oldest = k
					break
				}
				if oldest == "" || o.v.builtAt.Before(ot) {
					oldest, ot = k, o.v.builtAt
				}
			}
			delete(c.m, oldest)
		}
		e = &vaultEntry{}
		c.m[user] = e
	}
	return e
}

// trim evicts the oldest vaults (never keep) until the cache fits its byte
// budget. Evicted users simply rebuild on their next visit.
func (c *vaultCache) trim(keep string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		total := 0
		var oldest string
		var ot time.Time
		for k, o := range c.m {
			if o.v == nil {
				continue
			}
			total += o.v.bytes
			if k != keep && (oldest == "" || o.v.builtAt.Before(ot)) {
				oldest, ot = k, o.v.builtAt
			}
		}
		if total <= vaultCacheByte || oldest == "" {
			return
		}
		delete(c.m, oldest)
	}
}

// vaultVersion is a cheap fingerprint of everything a vault page is built
// from: counts and latest change times of the user's live entities,
// relationships, memories and memory links.
func (s *server) vaultVersion(ctx context.Context, userID string) (string, error) {
	var e, m int64
	var et, mt, rh, mh string
	err := s.st.DB.QueryRowContext(ctx, `
SELECT
 (SELECT count(*) FROM entities WHERE user_id=$1 AND deleted_at IS NULL),
 (SELECT coalesce(max(updated_at)::text,'') FROM entities WHERE user_id=$1 AND deleted_at IS NULL),
 (SELECT coalesce(md5(string_agg(id||'|'||source_id||'|'||target_id||'|'||type, ',' ORDER BY id)),'') FROM relationships WHERE user_id=$1 AND deleted_at IS NULL),
 (SELECT count(*) FROM memories WHERE user_id=$1 AND deleted_at IS NULL),
 (SELECT coalesce(max(updated_at)::text,'') FROM memories WHERE user_id=$1 AND deleted_at IS NULL),
 (SELECT coalesce(md5(string_agg(memory_id||'|'||entity_id, ',' ORDER BY memory_id, entity_id)),'') FROM memory_entities WHERE user_id=$1)`, userID).Scan(&e, &et, &rh, &m, &mt, &mh)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d|%s|%s|%d|%s|%s", e, et, rh, m, mt, mh), nil
}

// vaultBuildSem caps simultaneous vault builds process-wide.
var vaultBuildSem = make(chan struct{}, 2)

// vaultFor returns the user's vault, rebuilding it when their data changed.
// It only ever builds from the signed-in user's own scope.
func (s *server) vaultFor(ctx context.Context, userID string) (*vaultSet, error) {
	ver, err := s.vaultVersion(ctx, userID)
	if err != nil {
		return nil, err
	}
	e := s.vcache.entry(userID)
	e.mu.Lock()
	defer e.mu.Unlock()
	now := s.now()
	if e.v != nil && e.version == ver && now.Sub(e.v.builtAt) < vaultCacheTTL {
		return e.v, nil
	}
	select { // bound concurrent builds across all users (memory)
	case vaultBuildSem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	set, err := vault.Build(ctx, s.root.ForUser(userID), now.UTC())
	<-vaultBuildSem
	if err != nil {
		return nil, err
	}
	for _, werr := range set.Errors {
		s.cfg.Logger.Warn("vault build", "err", werr)
	}
	e.v, e.version = newVaultSet(set, now), ver
	s.vcache.trim(userID)
	return e.v, nil
}

func (s *server) mountVault(mux *http.ServeMux) {
	s.zipLimit = accounts.NewLimiter(vaultZipPerMin, 3, s.now)
	mux.HandleFunc("GET /vault", s.auth(s.vaultList))
	mux.HandleFunc("GET /vault/download", s.auth(s.vaultDownload))
	mux.HandleFunc("GET /vault/{path...}", s.auth(s.vaultPage))
}

type vaultRow struct {
	Path, Base, Name, Folder, Type, Ago, Abs, ISO string
}

type vaultTab struct {
	Label, Href string
	Current     bool
}

var folderSingular = map[string]string{
	"people": "Person", "organizations": "Organisation", "concepts": "Project",
	"events": "Event", "documents": "Document", "sources": "Page",
}

type vaultListView struct {
	Folder, FolderLabel string
	Q, Sort             string
	Total, All          int
	Rows                []vaultRow
	Folders             []folderCount
	Tabs                []vaultTab
	Page, Prev, Next    int
	Query               string // encoded filters for paging links, without page
	HasIndex            bool
	Filtered            bool
}

func (s *server) vaultList(w http.ResponseWriter, r *http.Request, u SessionUser) {
	vs, err := s.vaultFor(r.Context(), u.UserID)
	if err != nil {
		s.cfg.Logger.Error("vault", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	q := r.URL.Query()
	v := vaultListView{Q: strings.TrimSpace(q.Get("q")), Sort: q.Get("sort"), Folder: q.Get("folder"), Folders: vs.folders}
	if v.Sort != "updated" {
		v.Sort = "name"
	}
	valid := v.Folder == "index"
	for _, f := range vs.folders {
		if f.Key == v.Folder {
			valid = true
			v.FolderLabel = f.Label
		}
	}
	if !valid {
		v.Folder = ""
	}
	if v.Folder == "index" {
		v.FolderLabel = "Index"
	}
	v.Page, _ = strconv.Atoi(q.Get("page"))
	if v.Page < 1 {
		v.Page = 1
	}
	v.All = len(vs.set.Files)
	needle := strings.ToLower(v.Q)
	var files []vault.File
	for _, f := range vs.set.Files {
		switch {
		case v.Folder == "index" && f.Kind != vault.KindIndex:
			continue
		case v.Folder != "" && v.Folder != "index" && f.Folder != v.Folder:
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(f.Path), needle) && !strings.Contains(strings.ToLower(f.Name), needle) {
			continue
		}
		files = append(files, f)
	}
	sort.SliceStable(files, func(i, j int) bool {
		a, b := files[i], files[j]
		if (a.Kind == vault.KindIndex) != (b.Kind == vault.KindIndex) {
			return a.Kind == vault.KindIndex
		}
		if v.Sort == "updated" && !a.Updated.Equal(b.Updated) {
			return a.Updated.After(b.Updated)
		}
		return a.Path < b.Path
	})
	v.Total = len(files)
	v.Filtered = v.Q != "" || v.Folder != ""
	lo := (v.Page - 1) * vaultPerPage
	if lo >= len(files) {
		lo = 0
		v.Page = 1
	}
	hi := min(lo+vaultPerPage, len(files))
	now := s.now()
	for _, f := range files[lo:hi] {
		row := vaultRow{Path: f.Path, Base: f.Path, Name: f.Name, Folder: f.Folder, Type: rowType(f)}
		if !f.Updated.IsZero() {
			row.Ago, row.Abs, row.ISO = relTime(now, f.Updated), absTime(f.Updated), f.Updated.UTC().Format(time.RFC3339)
		}
		v.Rows = append(v.Rows, row)
	}
	if v.Page > 1 {
		v.Prev = v.Page - 1
	}
	if hi < len(files) {
		v.Next = v.Page + 1
	}
	qs := url.Values{}
	if v.Q != "" {
		qs.Set("q", v.Q)
	}
	if v.Folder != "" {
		qs.Set("folder", v.Folder)
	}
	if v.Sort != "name" {
		qs.Set("sort", v.Sort)
	}
	v.Query = qs.Encode()
	v.Tabs = vaultTabs(vs, v.Folder, v.Q)
	s.render(w, r, http.StatusOK, "vault", v)
}

type vaultPageView struct {
	Path, Title, Folder, FolderLabel string
	Props                            []prop
	Details                          []prop // identifiers, folded away
	Exported                         exportedAt
	Body                             template.HTML
	Source                           string
	ShowSource                       bool
	Backlinks                        []backlink
	GraphHref, MemoriesHref          string
	Ago, Abs, ISO                    string
	Kind                             string
}

func (s *server) vaultPage(w http.ResponseWriter, r *http.Request, u SessionUser) {
	p := r.PathValue("path")
	if !strings.HasSuffix(p, ".md") {
		s.vaultMissing(w, r)
		return
	}
	vs, err := s.vaultFor(r.Context(), u.UserID)
	if err != nil {
		s.cfg.Logger.Error("vault", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// The path is only ever a key into this user's own generated files.
	f, ok := vs.file(p)
	if !ok {
		s.vaultMissing(w, r)
		return
	}
	front, body := splitFrontmatter(f.Content)
	title := f.Name
	if t, rest, ok := strings.Cut(body, "\n"); ok && strings.HasPrefix(t, "# ") {
		title, body = strings.TrimPrefix(t, "# "), rest
	}
	body = dropSection(body, "Backlinks")
	if _, ok := vs.resolve("log"); !ok {
		// The exporter's index points at a log page this vault does not have.
		body = strings.ReplaceAll(body, "See also: [[log]]\n", "")
	}
	props, exported := splitExported(parseProps(front))
	props, details := s.friendlyProps(props)
	body = humanizeRelLines(body)
	v := vaultPageView{
		Exported: s.exportedAt(exported),
		Details:  details,
		Path:     f.Path, Title: title, Folder: f.Folder, FolderLabel: folderLabel(f.Folder), Kind: string(f.Kind),
		Props:      props,
		Body:       renderMarkdown(body, vs.resolve),
		Source:     f.Content,
		ShowSource: r.URL.Query().Get("view") == "source",
		Backlinks:  vs.backlinks[f.Path],
	}
	if f.Kind == vault.KindEntity {
		v.GraphHref = "/graph?center=" + url.QueryEscape(f.EntityID)
		v.MemoriesHref = "/memories?entity=" + url.QueryEscape(f.EntityID)
	}
	if !f.Updated.IsZero() {
		v.Ago, v.Abs, v.ISO = relTime(s.now(), f.Updated), absTime(f.Updated), f.Updated.UTC().Format(time.RFC3339)
	}
	s.render(w, r, http.StatusOK, "vault_page", v)
}

// dropSection removes a "## name" section from markdown; the page shows the
// computed backlinks instead.
func dropSection(md, name string) string {
	h := "\n## " + name + "\n"
	i := strings.Index(md, h)
	if i < 0 {
		return md
	}
	rest := md[i+len(h):]
	if j := strings.Index(rest, "\n## "); j >= 0 {
		return md[:i] + rest[j:]
	}
	return md[:i] + "\n"
}

func (s *server) vaultDownload(w http.ResponseWriter, r *http.Request, u SessionUser) {
	if !s.zipLimit.Allow(u.UserID) {
		w.Header().Set("Retry-After", "30")
		http.Error(w, "Too many downloads. Try again in a minute.", http.StatusTooManyRequests)
		return
	}
	vs, err := s.vaultFor(r.Context(), u.UserID)
	if err != nil {
		s.cfg.Logger.Error("vault", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", `attachment; filename="cortex-vault-`+s.now().UTC().Format("2006-01-02")+`.zip"`)
	h.Set("Cache-Control", "private, no-store")
	zw := zip.NewWriter(w)
	for _, f := range vs.set.Files {
		if !safeZipName(f.Path) {
			continue
		}
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: f.Path, Method: zip.Deflate, Modified: f.Updated})
		if err == nil {
			_, err = fw.Write([]byte(f.Content))
		}
		if err != nil {
			s.cfg.Logger.Warn("vault zip", "err", err)
			return
		}
	}
	if err := zw.Close(); err != nil {
		s.cfg.Logger.Warn("vault zip", "err", err)
	}
}

// safeZipName rejects entry names that could extract outside the target
// folder or carry control characters.
func safeZipName(n string) bool {
	if n == "" || strings.HasPrefix(n, "/") || strings.ContainsAny(n, "\\") {
		return false
	}
	for _, r := range n {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	for _, seg := range strings.Split(n, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// splitExported lifts the exporter's exported_at stamp out of the properties
// so pages open with content, not export bookkeeping.
func splitExported(in []prop) (out []prop, exported string) {
	for _, p := range in {
		if p.Key == "exported_at" && !p.Nested {
			exported = p.Value
			continue
		}
		out = append(out, p)
	}
	return out, exported
}

// rowType is the quiet type label under a page title in the list.
func rowType(f vault.File) string {
	if f.Kind == vault.KindIndex {
		return "Overview"
	}
	if l, ok := folderSingular[f.Folder]; ok {
		return l
	}
	return folderLabel(f.Folder)
}

// vaultTabs builds the segmented type row: All plus the fixed page types,
// and Pages only when the vault has any. A search term carries across tabs.
func vaultTabs(vs *vaultSet, folder, q string) []vaultTab {
	href := func(key string) string {
		qs := url.Values{}
		if key != "" {
			qs.Set("folder", key)
		}
		if q != "" {
			qs.Set("q", q)
		}
		if e := qs.Encode(); e != "" {
			return "/vault?" + e
		}
		return "/vault"
	}
	tabs := []vaultTab{{"All", href(""), folder == "" || folder == "index"}}
	for _, k := range folderOrder {
		tabs = append(tabs, vaultTab{folderLabel(k), href(k), folder == k})
	}
	for _, f := range vs.folders {
		if f.Key == "sources" {
			tabs = append(tabs, vaultTab{folderLabel("sources"), href("sources"), folder == "sources"})
		}
	}
	return tabs
}

type vaultMissingView struct{ Path string }

// vaultMissing is the friendly 404 for a vault page that does not exist.
func (s *server) vaultMissing(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusNotFound, "vault_missing", vaultMissingView{})
}

// relLabelIf humanizes a relationship word, leaving "" (not a relationship) alone.
func relLabelIf(t string) string {
	if t == "" {
		return ""
	}
	return relLabel(t)
}

// humanizeRelLines rewrites the exporter's "- works_at → [[x]]" lines as
// "- works at → [[x]]" so the page reads in words.
func humanizeRelLines(md string) string {
	lines := strings.Split(md, "\n")
	for i, ln := range lines {
		if !strings.HasPrefix(ln, "- ") {
			continue
		}
		if t, rest, ok := strings.Cut(strings.TrimPrefix(ln, "- "), " → "); ok && !strings.Contains(t, " ") && strings.Contains(t, "_") && strings.HasPrefix(rest, "[[") {
			lines[i] = "- " + relLabel(t) + " → " + rest
		}
	}
	return strings.Join(lines, "\n")
}

// exportedAt is a vault timestamp shown like dates elsewhere: relative text
// with the exact time in the title. Raw holds an unparseable stamp as-is.
type exportedAt struct{ Ago, Abs, ISO, Raw string }

func (e exportedAt) Any() bool { return e.Ago != "" || e.Raw != "" }

func (s *server) exportedAt(raw string) exportedAt {
	if raw == "" {
		return exportedAt{}
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return exportedAt{Raw: raw}
	}
	return exportedAt{Ago: relTime(s.now(), t), Abs: absTime(t), ISO: t.UTC().Format(time.RFC3339)}
}

var propLabels = map[string]string{"created_at": "Created", "updated_at": "Updated"}

// friendlyProps turns raw frontmatter rows into display rows: plain labels,
// dates like the rest of the app, and identifiers moved into details.
func (s *server) friendlyProps(in []prop) (out, details []prop) {
	for _, p := range in {
		if !p.Nested && p.Key == "cortex_id" {
			p.Label = "Entity ID"
			details = append(details, p)
			continue
		}
		p.Label = propLabels[p.Key]
		if p.Label == "" {
			p.Label = strings.ReplaceAll(p.Key, "_", " ")
			if p.Label != "" {
				p.Label = strings.ToUpper(p.Label[:1]) + p.Label[1:]
			}
		}
		if p.Key == "created_at" || p.Key == "updated_at" {
			if e := s.exportedAt(p.Value); e.Ago != "" {
				p.When = &e
			}
		}
		out = append(out, p)
	}
	return out, details
}
