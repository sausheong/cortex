package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"
)

const (
	memoriesPerPage  = 50
	clearBrainPhrase = "delete my brain"
)

func (s *server) mountMemories(mux *http.ServeMux) {
	mux.HandleFunc("GET /memories", s.auth(s.memoriesGet))
	mux.HandleFunc("POST /memories/delete", s.auth(s.memoriesDelete))
	mux.HandleFunc("GET /memories/deleted", s.auth(s.memoriesDeleted))
	mux.HandleFunc("POST /memories/restore/{batch}", s.auth(s.memoriesRestore))
	mux.HandleFunc("GET /account", s.auth(s.accountGet))
	mux.HandleFunc("POST /account/delete", s.auth(s.accountDelete))
}

type memoriesView struct {
	Memories    []cortex.Memory
	Conns       []accounts.Connection
	ConnNames   map[string]string
	Q, By       string
	From, To    string
	Total       int
	Page        int
	PrevPage    int
	NextPage    int
	Query       string // url-encoded filters for paging links
	Error       string
	DeletedFlag int
	Restored    bool
}

func parseDay(v string) *time.Time {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(v))
	if err != nil {
		return nil
	}
	return &t
}

func (s *server) memoriesGet(w http.ResponseWriter, r *http.Request, u SessionUser) {
	s.memoriesPage(w, r, u, http.StatusOK, "")
}

func (s *server) memoriesPage(w http.ResponseWriter, r *http.Request, u SessionUser, status int, msg string) {
	ctx := r.Context()
	q := r.URL.Query()
	v := memoriesView{Q: q.Get("q"), By: q.Get("by"), From: q.Get("from"), To: q.Get("to"), Error: msg}
	v.Page, _ = strconv.Atoi(q.Get("page"))
	if v.Page < 1 {
		v.Page = 1
	}
	v.DeletedFlag, _ = strconv.Atoi(q.Get("deleted"))
	v.Restored = q.Get("restored") == "1"
	f := cortex.MemoryListFilter{Query: v.Q, WrittenBy: v.By, From: parseDay(v.From)}
	if t := parseDay(v.To); t != nil {
		end := t.AddDate(0, 0, 1)
		f.To = &end
	}
	ms, total, err := s.root.ForUser(u.UserID).ListMemories(ctx, f, memoriesPerPage, (v.Page-1)*memoriesPerPage)
	if err != nil {
		s.cfg.Logger.Error("memories", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	conns, err := s.st.ListConnections(ctx, u.UserID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	v.Memories, v.Total, v.Conns = ms, total, conns
	v.ConnNames = map[string]string{}
	for _, c := range conns {
		v.ConnNames[c.ID] = c.Name
	}
	if v.Page > 1 {
		v.PrevPage = v.Page - 1
	}
	if v.Page*memoriesPerPage < total {
		v.NextPage = v.Page + 1
	}
	pq := make([]string, 0, 4)
	add := func(k, val string) {
		if val != "" {
			pq = append(pq, k+"="+url.QueryEscape(val))
		}
	}
	add("q", v.Q)
	add("by", v.By)
	add("from", v.From)
	add("to", v.To)
	v.Query = strings.Join(pq, "&")
	s.render(w, r, status, "memories", v)
}

func (s *server) memoriesDelete(w http.ResponseWriter, r *http.Request, u SessionUser) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	f := cortex.DeleteFilter{IDs: r.PostForm["ids"], WrittenBy: strings.TrimSpace(r.PostFormValue("by")),
		Query: strings.TrimSpace(r.PostFormValue("match"))}
	if d := strings.TrimSpace(r.PostFormValue("older_than")); d != "" {
		days, err := strconv.Atoi(d)
		if err != nil || days < 0 {
			s.memoriesPage(w, r, u, http.StatusBadRequest, "older_than must be a number of days")
			return
		}
		t := s.now().UTC().AddDate(0, 0, -days)
		f.OlderThan = &t
	}
	if r.PostFormValue("all") == "1" {
		if strings.TrimSpace(r.PostFormValue("confirm")) != clearBrainPhrase {
			s.memoriesPage(w, r, u, http.StatusBadRequest, "Type \""+clearBrainPhrase+"\" to delete everything.")
			return
		}
		f.All = true
	}
	if len(f.IDs) == 0 && f.WrittenBy == "" && f.Query == "" && f.OlderThan == nil && !f.All {
		s.memoriesPage(w, r, u, http.StatusBadRequest, "Choose what to delete.")
		return
	}
	_, n, err := s.root.ForUser(u.UserID).SoftDeleteMemories(r.Context(), f, "web:"+u.UserID)
	if err != nil {
		s.cfg.Logger.Error("memories delete", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/memories/deleted?deleted="+strconv.Itoa(n), http.StatusSeeOther)
}

type deletedView struct {
	Batches []cortex.DeleteBatch
	Deleted int
}

func (s *server) memoriesDeleted(w http.ResponseWriter, r *http.Request, u SessionUser) {
	bs, err := s.root.ForUser(u.UserID).ListDeleteBatches(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("deleted"))
	s.render(w, r, http.StatusOK, "deleted", deletedView{Batches: bs, Deleted: n})
}

func (s *server) memoriesRestore(w http.ResponseWriter, r *http.Request, u SessionUser) {
	err := s.root.ForUser(u.UserID).RestoreBatch(r.Context(), r.PathValue("batch"))
	if errors.Is(err, cortex.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.cfg.Logger.Error("restore", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/memories?restored=1", http.StatusSeeOther)
}

type accountView struct {
	Usage cortex.Usage
	MB    string
}

func (s *server) accountGet(w http.ResponseWriter, r *http.Request, u SessionUser) {
	us, err := s.root.ForUser(u.UserID).Usage(r.Context())
	if err != nil {
		s.cfg.Logger.Error("usage", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.render(w, r, http.StatusOK, "account", accountView{Usage: us, MB: strconv.FormatFloat(float64(us.Bytes)/(1<<20), 'f', 2, 64)})
}

func (s *server) accountDelete(w http.ResponseWriter, r *http.Request, u SessionUser) {
	if err := s.st.DeleteAccount(r.Context(), u.UserID); err != nil {
		s.cfg.Logger.Error("delete account", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
