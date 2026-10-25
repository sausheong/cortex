package web

import (
	"archive/zip"
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/vaultart"
	"github.com/sausheong/cortex/internal/workset"
)

const vaultZipPerMin = 6

// vaultReq is what every Vault page needs for one request: the signed-in
// user's keyed handle, the decrypted working set and the article index built
// from it. release must be called when the handler is done.
type vaultReq struct {
	cx       *cortex.Cortex
	release  func()
	set      *workset.Set
	ix       *vaultart.Index
	settings vaultart.Settings
	ownerID  string
}

// vaultLoad opens the user's data for a Vault page. It answers the request
// itself (unlock redirect or an error) and reports false when it cannot.
func (s *server) vaultLoad(w http.ResponseWriter, r *http.Request, u SessionUser) (*vaultReq, bool) {
	ctx := r.Context()
	cx, release := providerCxFor(ctx, s.root, s.st, u)
	fail := func(err error) (*vaultReq, bool) {
		release()
		if errors.Is(err, model.ErrLocked) || errors.Is(err, model.ErrEnrollRequired) {
			s.vaultRedirect(w, r, "/unlock?next="+url.QueryEscape(safeNext(r.URL.RequestURI())), http.StatusSeeOther)
			return nil, false
		}
		s.cfg.Logger.Error("vault", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}
	set, settings, ownerID, ix, err := s.articleIndex(ctx, cx)
	if err != nil {
		return fail(err)
	}
	return &vaultReq{cx: cx, release: release, set: set, settings: settings, ownerID: ownerID,
		ix: ix.WithCanWrite(vaultart.CanWrite(cx))}, true
}

// articleIndex loads what the article index is built from and returns the
// index. It needs the unlocked handle; a locked one returns model.ErrLocked.
func (s *server) articleIndex(ctx context.Context, cx *cortex.Cortex) (*workset.Set, vaultart.Settings, string, *vaultart.Index, error) {
	h := cx.StoreHandle()
	set, err := workset.For(ctx, h)
	if err != nil {
		return nil, vaultart.Settings{}, "", nil, err
	}
	settings, err := vaultart.LoadSettings(ctx, h)
	if err != nil {
		return nil, vaultart.Settings{}, "", nil, err
	}
	ownerID, err := cx.OwnerEntityID(ctx)
	if err != nil {
		ownerID = ""
	}
	ix, err := vaultart.BuildIndex(ctx, h, set, ownerID, settings, s.now())
	if err != nil {
		return nil, vaultart.Settings{}, "", nil, err
	}
	return set, settings, ownerID, ix, nil
}

// articlePath returns the Vault path of the article for an entity (its own,
// or its topic's), or "" when there is none or the index cannot be built. It
// is how the Graph and Memories pages offer "See article"; a failure only
// means no link.
func (s *server) articlePath(ctx context.Context, cx *cortex.Cortex, entityID string) string {
	if entityID == "" {
		return ""
	}
	_, _, _, ix, err := s.articleIndex(ctx, cx)
	if err != nil {
		return ""
	}
	p, _ := ix.PathFor(entityID)
	return p
}

// vaultRedirect redirects with the same no-store policy as the pages.
func (s *server) vaultRedirect(w http.ResponseWriter, r *http.Request, to string, code int) {
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, to, code)
}

func (s *server) mountVault(mux *http.ServeMux) {
	s.zipLimit = accounts.NewLimiter(vaultZipPerMin, 3, s.now)
	mux.HandleFunc("GET /vault", s.authUnlocked(s.vaultHome))
	mux.HandleFunc("GET /vault/download", s.authUnlocked(s.vaultDownload))
	mux.HandleFunc("GET /vault/{group}/{slug}", s.authUnlocked(s.vaultArticle))
	mux.HandleFunc("POST /vault/{group}/{slug}/rewrite", s.authUnlocked(s.vaultRewrite))
	mux.HandleFunc("POST /vault/{group}/{slug}/retry", s.authUnlocked(s.vaultRetry))
	mux.HandleFunc("POST /vault/{group}/{slug}/correct", s.authUnlocked(s.vaultCorrect))
	mux.HandleFunc("POST /vault/{group}/{slug}/undo", s.authUnlocked(s.vaultUndo))
	mux.HandleFunc("GET /vault/{path...}", s.authUnlocked(s.vaultLegacy))
}

// vaultRow is one line of Vault home.
type vaultRow struct {
	Path, Name, Descriptor string
	Written                bool
	Ago, Abs, ISO          string
	Note                   string // why it is under Recently changed
}

type vaultSection struct {
	Heading string
	Rows    []vaultRow
}

type vaultHomeView struct {
	Q         string
	Searching bool
	Sections  []vaultSection
	Recent    []vaultRow
	CanWrite  bool
	Threshold int
	Empty     bool // nothing qualifies at all
}

// vaultRows builds display rows; notes says why a row is under Recently changed.
func (s *server) vaultRows(in []vaultart.IndexEntry, notes bool) []vaultRow {
	now := s.now()
	out := make([]vaultRow, 0, len(in))
	for _, e := range in {
		row := vaultRow{Path: e.Path, Name: e.Name, Descriptor: e.Descriptor}
		if e.WrittenAt != nil {
			row.Written = true
			row.Ago, row.Abs, row.ISO = relTime(now, *e.WrittenAt), absTime(*e.WrittenAt), e.WrittenAt.UTC().Format(time.RFC3339)
		}
		switch {
		case !notes:
		case e.Changed:
			row.Note = "Changed since it was written"
		case e.NewlyQualified:
			row.Note = "New"
		}
		out = append(out, row)
	}
	return out
}

// vaultHome lists the articles from the graph alone: no model call, no
// article body opened.
func (s *server) vaultHome(w http.ResponseWriter, r *http.Request, u SessionUser) {
	vr, ok := s.vaultLoad(w, r, u)
	if !ok {
		return
	}
	defer vr.release()
	ix := vr.ix
	v := vaultHomeView{Q: strings.TrimSpace(r.URL.Query().Get("q")), CanWrite: ix.CanWrite, Threshold: vr.settings.Threshold}
	g := vaultart.Groups{People: ix.People, Projects: ix.Projects, Events: ix.Events, Topics: ix.Topics}
	v.Empty = g.Empty()
	if v.Q != "" {
		v.Searching = true
		g = ix.Search(v.Q)
	} else {
		// Only articles that are genuinely out of date: before anything is
		// written every entity is "new" and the list would just repeat the index.
		var changed []vaultart.IndexEntry
		for _, e := range ix.Recent {
			if e.Changed {
				changed = append(changed, e)
			}
		}
		v.Recent = s.vaultRows(changed, true)
	}
	add := func(heading string, in []vaultart.IndexEntry) {
		if len(in) > 0 {
			v.Sections = append(v.Sections, vaultSection{Heading: heading, Rows: s.vaultRows(in, false)})
		}
	}
	add("People", g.People)
	add("Projects and organisations", g.Projects)
	add("Events", g.Events)
	add("Topics", g.Topics)
	s.render(w, r, http.StatusOK, "vault", v)
}

type vaultMissingView struct{ Path string }

// vaultMissing is the friendly 404 for a vault page that does not exist.
func (s *server) vaultMissing(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusNotFound, "vault_missing", vaultMissingView{})
}

// vaultLegacy answers the old per-entity markdown URLs. A path that still has
// an article redirects there (302, since which entities qualify can change);
// index.md and log.md go to Vault home; anything else is the friendly 404.
func (s *server) vaultLegacy(w http.ResponseWriter, r *http.Request, u SessionUser) {
	p := r.PathValue("path")
	s.legacy(w, r, u, p)
}

func (s *server) legacy(w http.ResponseWriter, r *http.Request, u SessionUser, p string) {
	if !strings.HasSuffix(p, ".md") {
		s.vaultMissing(w, r)
		return
	}
	vr, ok := s.vaultLoad(w, r, u)
	if !ok {
		return
	}
	defer vr.release()
	if to, ok := vaultart.ResolveLegacy(p, vr.ix, vr.set); ok {
		s.vaultRedirect(w, r, to, http.StatusFound)
		return
	}
	s.vaultMissing(w, r)
}

// vaultDownload serves the Obsidian export built from the articles. It never
// calls the article writer: a subject without an article becomes a stub.
func (s *server) vaultDownload(w http.ResponseWriter, r *http.Request, u SessionUser) {
	if !s.zipLimit.Allow(u.UserID) {
		w.Header().Set("Retry-After", "30")
		http.Error(w, "Too many downloads. Try again in a minute.", http.StatusTooManyRequests)
		return
	}
	vr, ok := s.vaultLoad(w, r, u)
	if !ok {
		return
	}
	files, err := vaultart.BuildExport(r.Context(), vr.cx.StoreHandle(), vr.set, vr.ownerID, vr.settings, s.now())
	vr.release()
	if err != nil {
		if errors.Is(err, model.ErrLocked) || errors.Is(err, model.ErrEnrollRequired) {
			s.vaultRedirect(w, r, "/unlock?next="+url.QueryEscape(safeNext(r.URL.RequestURI())), http.StatusSeeOther)
			return
		}
		s.cfg.Logger.Error("vault export", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", `attachment; filename="cortex-vault-`+s.now().UTC().Format("2006-01-02")+`.zip"`)
	h.Set("Cache-Control", "private, no-store")
	zw := zip.NewWriter(w)
	for _, f := range files {
		if !safeZipName(f.Path) {
			continue
		}
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: f.Path, Method: zip.Deflate, Modified: f.Modified})
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

// exportedAt is a vault timestamp shown like dates elsewhere: relative text
// with the exact time in the title. Raw holds an unparseable stamp as-is.
type exportedAt struct{ Ago, Abs, ISO, Raw string }

func (e exportedAt) Any() bool { return e.Ago != "" || e.Raw != "" }
