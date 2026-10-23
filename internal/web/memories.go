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
	clearAllPhrase   = "delete everything"
	deleteAcctPhrase = "delete my account"
)

func (s *server) mountMemories(mux *http.ServeMux) {
	mux.HandleFunc("GET /memories", s.auth(s.memoriesGet))
	mux.HandleFunc("POST /memories/delete", s.auth(s.memoriesDelete))
	mux.HandleFunc("GET /memories/deleted", s.auth(s.memoriesDeleted))
	mux.HandleFunc("POST /memories/restore/{batch}", s.auth(s.memoriesRestore))
	mux.HandleFunc("GET /account", s.auth(s.accountGet))
	mux.HandleFunc("POST /account/delete", s.auth(s.accountDelete))
}

// undoNotice is the persistent "Deleted N. Undo" message shown after a delete.
type undoNotice struct {
	BatchID string
	Count   int
	Next    string // where Undo returns to
}

// memoryRow is one memory prepared for display: relative time with the
// absolute time kept for a tooltip.
type memoryRow struct {
	ID, Content string
	Ago, Abs    string
	ISO         string
	Who         string // connection name, or "" when written by hand
	New         bool   // written since the viewer's previous visit
}

type memoriesView struct {
	Memories  []memoryRow
	Conns     []accounts.Connection
	ConnNames map[string]string
	Q, By     string
	From, To  string
	Total     int
	Page      int
	PrevPage  int
	NextPage  int
	Query     string // url-encoded filters for paging links
	// Error is shown next to the control named by ErrField
	// ("ids", "older_than", "match", "").
	Error, ErrField string
	Undo            *undoNotice
	Restored        int // memories restored by the last Undo
	WasRestored     bool
	HasMore         bool // a filter is active, so the filter row shows it
}

func parseDay(v string) *time.Time {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(v))
	if err != nil {
		return nil
	}
	return &t
}

func (s *server) memoriesGet(w http.ResponseWriter, r *http.Request, u SessionUser) {
	s.memoriesPage(w, r, u, http.StatusOK, "", "")
}

func (s *server) memoriesPage(w http.ResponseWriter, r *http.Request, u SessionUser, status int, field, msg string) {
	ctx := r.Context()
	q := r.URL.Query()
	v := memoriesView{Q: q.Get("q"), By: q.Get("by"), From: q.Get("from"), To: q.Get("to"), Error: msg, ErrField: field}
	v.Page, _ = strconv.Atoi(q.Get("page"))
	if v.Page < 1 {
		v.Page = 1
	}
	if n, err := strconv.Atoi(q.Get("restored")); err == nil {
		v.WasRestored, v.Restored = true, n
	}
	f := cortex.MemoryListFilter{Query: v.Q, WrittenBy: v.By, From: parseDay(v.From)}
	if t := parseDay(v.To); t != nil {
		end := t.AddDate(0, 0, 1)
		f.To = &end
	}
	cx := s.root.ForUser(u.UserID)
	ms, total, err := cx.ListMemories(ctx, f, memoriesPerPage, (v.Page-1)*memoriesPerPage)
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
	// The undo notice is built from the stored batch, never from the URL's
	// numbers, so a crafted link cannot show a false count.
	if b := q.Get("batch"); b != "" {
		if bs, err := cx.ListDeleteBatches(ctx); err == nil {
			for _, d := range bs {
				if d.ID == b && d.Kind == "memories" {
					v.Undo = &undoNotice{BatchID: d.ID, Count: d.Memories, Next: "/memories"}
				}
			}
		}
	}
	v.Total, v.Conns = total, conns
	v.ConnNames = map[string]string{}
	for _, c := range conns {
		v.ConnNames[c.ID] = c.Name
	}
	now := s.now()
	seen := s.lastVisit(w, r, now, v.Page == 1 && r.Method == http.MethodGet)
	for _, m := range ms {
		v.Memories = append(v.Memories, memoryRow{ID: m.ID, Content: m.Content,
			Ago: relTime(now, m.CreatedAt), Abs: absTime(m.CreatedAt), ISO: m.CreatedAt.UTC().Format(time.RFC3339),
			Who: v.ConnNames[m.WrittenBy], New: !seen.IsZero() && m.CreatedAt.After(seen)})
	}
	v.HasMore = v.Q != "" || v.By != "" || v.From != "" || v.To != ""
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

// describeFilter says in plain words what a delete filter selects.
func describeFilter(f cortex.DeleteFilter, days string, connName string) string {
	var parts []string
	if len(f.IDs) > 0 {
		parts = append(parts, "the memories you ticked")
	}
	if f.WrittenBy != "" {
		who := connName
		if who == "" {
			who = "one connection"
		}
		parts = append(parts, "written by "+who)
	}
	if f.OlderThan != nil {
		parts = append(parts, "older than "+plural(atoiOr(days), "day", "days"))
	}
	if f.Query != "" {
		parts = append(parts, "containing \""+f.Query+"\"")
	}
	if len(parts) == 0 {
		return "every memory you have"
	}
	return strings.Join(parts, ", ")
}

func atoiOr(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func (s *server) memoriesDelete(w http.ResponseWriter, r *http.Request, u SessionUser) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	scope := r.PostFormValue("scope") // "selected", "matching" or "all"
	days := strings.TrimSpace(r.PostFormValue("older_than"))
	f := cortex.DeleteFilter{IDs: r.PostForm["ids"], WrittenBy: strings.TrimSpace(r.PostFormValue("by")),
		Query: strings.TrimSpace(r.PostFormValue("match"))}
	if days != "" {
		n, err := strconv.Atoi(days)
		if err != nil || n < 0 {
			s.memoriesPage(w, r, u, http.StatusBadRequest, "older_than", "Enter a whole number of days, like 30.")
			return
		}
		t := s.now().UTC().AddDate(0, 0, -n)
		f.OlderThan = &t
	}
	f.All = r.PostFormValue("all") == "1"
	if len(f.IDs) == 0 && f.WrittenBy == "" && f.Query == "" && f.OlderThan == nil && !f.All {
		if scope == "selected" {
			s.memoriesPage(w, r, u, http.StatusBadRequest, "ids", "Tick at least one memory, then choose Delete selected.")
		} else {
			s.memoriesPage(w, r, u, http.StatusBadRequest, "match", "Choose who wrote it, how old it is, or text it contains.")
		}
		return
	}
	cx := s.root.ForUser(u.UserID)
	n, err := cx.CountMemories(r.Context(), f)
	if err != nil {
		s.cfg.Logger.Error("memories count", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if n == 0 {
		field := "match"
		if len(f.IDs) > 0 {
			field = "ids"
		}
		s.memoriesPage(w, r, u, http.StatusBadRequest, field, "No memories match, so nothing was deleted.")
		return
	}

	cv := confirmView{
		Title:      "Delete " + plural(n, "memory", "memories") + "?",
		Action:     "/memories/delete",
		Count:      n,
		Button:     "Delete " + plural(n, "memory", "memories"),
		CancelHref: "/memories",
	}
	if f.All && f.WrittenBy == "" && f.Query == "" && f.OlderThan == nil && len(f.IDs) == 0 {
		cv.Title = "Delete all " + plural(n, "memory", "memories") + "?"
		cv.Button = "Delete all " + plural(n, "memory", "memories")
		cv.Phrase = clearAllPhrase
		cv.Consequence = "This clears everything cortex remembers about you. You can restore it for 30 days."
	} else {
		cv.Consequence = "You can restore " + map[bool]string{true: "it", false: "them"}[n == 1] + " for 30 days."
		cn := ""
		if f.WrittenBy != "" {
			if conns, err := s.st.ListConnections(r.Context(), u.UserID); err == nil {
				for _, c := range conns {
					if c.ID == f.WrittenBy {
						cn = c.Name
					}
				}
			}
		}
		cv.Details = []string{"Selected: " + describeFilter(f, days, cn) + "."}
	}
	cv.Details = append(cv.Details, "Restore from Recently deleted. After 30 days they are removed for good.")
	cv.Fields = []confirmField{{"scope", scope}}
	for _, id := range f.IDs {
		cv.Fields = append(cv.Fields, confirmField{"ids", id})
	}
	for _, kv := range [][2]string{{"by", f.WrittenBy}, {"older_than", days}, {"match", f.Query}} {
		if kv[1] != "" {
			cv.Fields = append(cv.Fields, confirmField{kv[0], kv[1]})
		}
	}
	if f.All {
		cv.Fields = append(cv.Fields, confirmField{"all", "1"})
	}

	want, has := expectCount(r)
	if !has {
		s.renderConfirm(w, r, http.StatusOK, cv)
		return
	}
	if want != n {
		cv.Error = changedMsg(want, n, "memories")
		s.renderConfirm(w, r, http.StatusConflict, cv)
		return
	}
	if cv.Phrase != "" && !phraseOK(r, cv.Phrase) {
		cv.PhraseErr = "That did not match. Type " + cv.Phrase + " exactly, or cancel."
		cv.Error = "Nothing was deleted."
		s.renderConfirm(w, r, http.StatusBadRequest, cv)
		return
	}
	batch, deleted, err := cx.SoftDeleteMemories(r.Context(), f, "web:"+u.UserID)
	if err != nil {
		s.cfg.Logger.Error("memories delete", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if batch == "" || deleted == 0 {
		s.memoriesPage(w, r, u, http.StatusConflict, "", "Those memories were already gone, so nothing was deleted.")
		return
	}
	http.Redirect(w, r, "/memories?batch="+url.QueryEscape(batch), http.StatusSeeOther)
}

type deletedRow struct {
	ID, Kind, Ago, Abs, ISO string
	Memories, Other         int
}

// deletedKind turns a delete-batch kind into plain words.
func deletedKind(k string) string {
	switch k {
	case "memories":
		return "Memories"
	case "connection":
		return "Revoked connection"
	case "compaction":
		return "Compaction"
	case "forget", "entity":
		return "Forgotten item"
	}
	return "Deletion"
}

type deletedView struct {
	Batches []deletedRow
}

func (s *server) memoriesDeleted(w http.ResponseWriter, r *http.Request, u SessionUser) {
	bs, err := s.root.ForUser(u.UserID).ListDeleteBatches(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	now := s.now()
	v := deletedView{}
	for _, b := range bs {
		v.Batches = append(v.Batches, deletedRow{ID: b.ID, Kind: deletedKind(b.Kind), Memories: b.Memories, Other: b.Entities + b.Relationships,
			Ago: relTime(now, b.CreatedAt), Abs: absTime(b.CreatedAt), ISO: b.CreatedAt.UTC().Format(time.RFC3339)})
	}
	s.render(w, r, http.StatusOK, "deleted", v)
}

func (s *server) memoriesRestore(w http.ResponseWriter, r *http.Request, u SessionUser) {
	cx := s.root.ForUser(u.UserID)
	id := r.PathValue("batch")
	n := 0
	if bs, err := cx.ListDeleteBatches(r.Context()); err == nil {
		for _, b := range bs {
			if b.ID == id {
				n = b.Memories
			}
		}
	}
	err := cx.RestoreBatch(r.Context(), id)
	if errors.Is(err, cortex.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.cfg.Logger.Error("restore", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	dest := "/memories"
	if r.PostFormValue("next") == "/connections" {
		dest = "/connections"
	}
	http.Redirect(w, r, dest+"?restored="+strconv.Itoa(n), http.StatusSeeOther)
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
	ctx := r.Context()
	n, err := s.root.ForUser(u.UserID).CountMemories(ctx, cortex.DeleteFilter{All: true})
	if err != nil {
		s.cfg.Logger.Error("account count", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	live := 0
	if conns, err := s.st.ListConnections(ctx, u.UserID); err == nil {
		for _, c := range conns {
			if c.RevokedAt == nil {
				live++
			}
		}
	}
	cv := confirmView{
		Title:       "Delete your account?",
		Consequence: "This signs you out and disconnects " + plural(live, "assistant or agent", "assistants and agents") + " right away. Your " + plural(n, "memory", "memories") + " are kept for 30 days.",
		Details:     []string{"Sign in again within 30 days to get your memories back. Connections do not come back; you connect them again. After 30 days everything is removed for good."},
		Action:      "/account/delete",
		Count:       n,
		Phrase:      deleteAcctPhrase,
		Button:      "Delete my account",
		CancelHref:  "/account",
	}
	want, has := expectCount(r)
	if !has {
		s.renderConfirm(w, r, http.StatusOK, cv)
		return
	}
	if want != n {
		cv.Error = changedMsg(want, n, "memories")
		s.renderConfirm(w, r, http.StatusConflict, cv)
		return
	}
	if !phraseOK(r, cv.Phrase) {
		cv.PhraseErr = "That did not match. Type " + cv.Phrase + " exactly, or cancel."
		cv.Error = "Your account was not deleted."
		s.renderConfirm(w, r, http.StatusBadRequest, cv)
		return
	}
	if err := s.st.DeleteAccount(ctx, u.UserID); err != nil {
		s.cfg.Logger.Error("delete account", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
