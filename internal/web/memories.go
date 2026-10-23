package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"
)

const (
	maxPageParam     = 1 << 24 // beyond this the clamp-to-last logic applies; avoids offset overflow
	defaultPageSize  = 50
	tzCookie         = "cortex_tz"
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

type connOpt struct{ ID, Label string }

type memoriesView struct {
	Memories []memoryRow
	Conns    []accounts.Connection
	// ConnOpts feeds the "Written by" selects: removed connections appear
	// only while they still have live memories.
	ConnOpts  []connOpt
	ConnNames map[string]string
	Q, By     string
	// Entity limits the list to memories that mention one entity; EntityName
	// is its name, empty when it is not the viewer's.
	Entity, EntityName string
	From, To           string
	Since              string // RFC3339 instant; "" when not filtering by it
	SinceAgo           string
	Total              int
	Page               int
	PrevPage           int
	NextPage           int
	Query              string // url-encoded filters for paging links
	ViewQuery          string // filters plus page, carried through delete forms
	Size               int
	Sizes              []int
	Sort               string // "newest" or "oldest"
	Range              string // preset key, or ""
	Presets            []preset
	TZHint             string
	RangeErr           string
	Pages              []pageLink
	PrevHref           string
	NextHref           string
	ShowFrom           int // 1-based index of the first row shown
	ShowTo             int
	TotalFmt           string
	ShowFmt            string
	// Error is shown next to the control named by ErrField
	// ("ids", "older_than", "match", "").
	Error, ErrField string
	Undo            *undoNotice
	Restored        int // memories restored by the last Undo
	WasRestored     bool
	HasMore         bool // a filter is active, so the filter row shows it
	// SelectPage pre-ticks every row (the no-JS "Select all on this page").
	SelectPage bool
	SelCount   int    // rows on this page
	SelectHref string // same page, all rows ticked
	PageHref   string // same page, nothing ticked
}

type preset struct {
	Label, Href string
	Current     bool
}

// pageLink is one entry in the numbered pager; Gap marks an ellipsis.
type pageLink struct {
	N       int
	Href    string
	Current bool
	Gap     bool
}

var pageSizes = []int{25, 50, 100, 200}

// parseSize accepts only the offered sizes; anything else falls back to the default.
func parseSize(v string) int {
	n, _ := strconv.Atoi(v)
	for _, s := range pageSizes {
		if n == s {
			return n
		}
	}
	return defaultPageSize
}

// fmtInt groups thousands: 1274 -> "1,274".
func fmtInt(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// tzOffset reads the browser-sent offset (minutes, as Date.getTimezoneOffset:
// UTC minus local). Missing or out of range means UTC.
func tzOffset(r *http.Request) (int, bool) {
	c, err := r.Cookie(tzCookie)
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(c.Value)
	if err != nil || n < -840 || n > 720 {
		return 0, false
	}
	return n, true
}

func tzLabel(off int, ok bool) string {
	if !ok || off == 0 {
		return "UTC"
	}
	sign, m := "+", -off
	if m < 0 {
		sign, m = "-", -m
	}
	l := "UTC" + sign + strconv.Itoa(m/60)
	if m%60 != 0 {
		l += ":" + fmt.Sprintf("%02d", m%60)
	}
	return l
}

// pageWindow lists page numbers around the current page, with gaps.
func pageWindow(cur, last int, href func(int) string) []pageLink {
	var out []pageLink
	prev := 0
	for n := 1; n <= last; n++ {
		if n != 1 && n != last && (n < cur-2 || n > cur+2) {
			continue
		}
		if prev != 0 && n-prev > 1 {
			out = append(out, pageLink{Gap: true})
		}
		out = append(out, pageLink{N: n, Href: href(n), Current: n == cur})
		prev = n
	}
	return out
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

// viewKeys are the only parameters a carried view may hold.
var viewKeys = []string{"q", "by", "from", "to", "range", "size", "sort", "page", "entity", "since"}

// cleanView parses a carried view string keeping only known keys, so the
// value can never redirect elsewhere or inject other parameters.
func cleanView(raw string) url.Values {
	in, _ := url.ParseQuery(raw)
	out := url.Values{}
	for _, k := range viewKeys {
		if v := in.Get(k); v != "" {
			out.Set(k, v)
		}
	}
	return out
}

// viewURL is /memories with the viewer's filters from a POST, optionally plus extra params.
func viewURL(r *http.Request, extra ...string) string {
	q := cleanView(r.PostFormValue("view"))
	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	if len(q) == 0 {
		return "/memories"
	}
	return "/memories?" + q.Encode()
}

func (s *server) memoriesPage(w http.ResponseWriter, r *http.Request, u SessionUser, status int, field, msg string) {
	ctx := r.Context()
	q := r.URL.Query()
	if r.Method == http.MethodPost {
		q = cleanView(r.PostFormValue("view"))
	}
	v := memoriesView{Q: q.Get("q"), By: q.Get("by"), From: q.Get("from"), To: q.Get("to"), Error: msg, ErrField: field}
	pg, perr := strconv.Atoi(q.Get("page"))
	if perr != nil && errors.Is(perr, strconv.ErrRange) && !strings.HasPrefix(q.Get("page"), "-") {
		pg = maxPageParam
	}
	v.Page = min(pg, maxPageParam)
	if v.Page < 1 {
		v.Page = 1
	}
	v.Size, v.Sizes = parseSize(q.Get("size")), pageSizes
	v.Sort = "newest"
	if q.Get("sort") == "oldest" {
		v.Sort = "oldest"
	}
	if n, err := strconv.Atoi(q.Get("restored")); err == nil {
		v.WasRestored, v.Restored = true, n
	}
	// Dates are whole days in the viewer's local time: the browser sends its
	// UTC offset in a cookie; without it we use UTC and say so.
	off, haveTZ := tzOffset(r)
	v.TZHint = tzLabel(off, haveTZ)
	shift := time.Duration(off) * time.Minute
	today := s.now().UTC().Add(-shift)
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	switch rng := q.Get("range"); rng {
	case "7d", "30d", "year":
		v.Range = rng
		from := today.AddDate(0, 0, -6)
		if rng == "30d" {
			from = today.AddDate(0, 0, -29)
		} else if rng == "year" {
			from = time.Date(today.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
		}
		v.From, v.To = from.Format("2006-01-02"), today.Format("2006-01-02")
	}
	v.Entity = strings.TrimSpace(q.Get("entity"))
	var since *time.Time
	if t, err := time.Parse(time.RFC3339, q.Get("since")); err == nil {
		t = t.UTC()
		since, v.Since = &t, t.Format(time.RFC3339)
		v.SinceAgo = relTime(s.now(), t)
	}
	f := cortex.MemoryListFilter{Query: v.Q, WrittenBy: v.By, EntityID: v.Entity, Since: since, Oldest: v.Sort == "oldest"}
	badDate := false
	if t := parseDay(v.From); t != nil {
		start := t.Add(shift)
		f.From = &start
	} else if v.From != "" {
		v.From, badDate = "", true
	}
	if t := parseDay(v.To); t != nil {
		end := t.AddDate(0, 0, 1).Add(shift)
		f.To = &end
	} else if v.To != "" {
		v.To, badDate = "", true
	}
	if badDate {
		v.RangeErr = "A date was not understood, so it was ignored. Pick dates with the date fields."
	}
	if f.From != nil && f.To != nil && !f.From.Before(*f.To) {
		v.RangeErr = "From is after To, so no memories can match. Change one of the dates."
	}
	cx := s.root.ForUser(u.UserID)
	ms, total, err := cx.ListMemories(ctx, f, v.Size, (v.Page-1)*v.Size)
	if err == nil && total > 0 && (v.Page-1)*v.Size >= total {
		// Never show a page beyond the last.
		v.Page = (total + v.Size - 1) / v.Size
		ms, total, err = cx.ListMemories(ctx, f, v.Size, (v.Page-1)*v.Size)
	}
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
	if v.Entity != "" {
		if e, err := cx.GetEntity(ctx, v.Entity); err == nil {
			v.EntityName = e.Name
		}
	}
	v.ConnNames = map[string]string{}
	for _, c := range conns {
		v.ConnNames[c.ID] = c.Name
		switch {
		case c.RemovedAt == nil:
			v.ConnOpts = append(v.ConnOpts, connOpt{c.ID, c.Name})
		case c.Written > 0 || c.ID == v.By:
			v.ConnOpts = append(v.ConnOpts, connOpt{c.ID, c.Name + " (removed)"})
		}
	}
	now := s.now()
	seen := s.lastVisit(w, r, now, v.Page == 1 && r.Method == http.MethodGet)
	for _, m := range ms {
		v.Memories = append(v.Memories, memoryRow{ID: m.ID, Content: m.Content,
			Ago: relTime(now, m.CreatedAt), Abs: absTime(m.CreatedAt), ISO: m.CreatedAt.UTC().Format(time.RFC3339),
			Who: v.ConnNames[m.WrittenBy], New: !seen.IsZero() && m.CreatedAt.After(seen)})
	}
	v.HasMore = v.Entity != "" || v.Since != "" || v.Q != "" || v.By != "" || v.From != "" || v.To != ""
	last := (total + v.Size - 1) / v.Size
	if v.Page > 1 {
		v.PrevPage = v.Page - 1
	}
	if v.Page < last {
		v.NextPage = v.Page + 1
	}
	// base carries every filter but not the page, so each link keeps the view.
	pq := make([]string, 0, 8)
	add := func(k, val string) {
		if val != "" {
			pq = append(pq, k+"="+url.QueryEscape(val))
		}
	}
	add("q", v.Q)
	add("entity", v.Entity)
	add("by", v.By)
	add("since", v.Since)
	if v.Range != "" {
		add("range", v.Range)
	} else {
		add("from", v.From)
		add("to", v.To)
	}
	if v.Size != defaultPageSize {
		add("size", strconv.Itoa(v.Size))
	}
	if v.Sort == "oldest" {
		add("sort", "oldest")
	}
	v.Query = strings.Join(pq, "&")
	href := func(n int) string {
		h := "/memories?" + v.Query
		if n > 1 {
			if v.Query != "" {
				h += "&"
			}
			h += "page=" + strconv.Itoa(n)
		}
		return strings.TrimSuffix(h, "?")
	}
	if v.PrevPage > 0 {
		v.PrevHref = href(v.PrevPage)
	}
	if v.NextPage > 0 {
		v.NextHref = href(v.NextPage)
	}
	if last > 1 {
		v.Pages = pageWindow(v.Page, last, href)
	}
	if len(ms) > 0 {
		v.ShowFrom = (v.Page-1)*v.Size + 1
		v.ShowTo = v.ShowFrom + len(ms) - 1
		v.ShowFmt = fmtInt(v.ShowFrom) + "\u2013" + fmtInt(v.ShowTo)
	}
	v.TotalFmt = fmtInt(total)
	// Presets keep search and author but replace the dates.
	presetHref := func(rng string) string {
		p := make([]string, 0, 6)
		for _, kv := range [][2]string{{"q", v.Q}, {"entity", v.Entity}, {"by", v.By}, {"since", v.Since}} {
			if kv[1] != "" {
				p = append(p, kv[0]+"="+url.QueryEscape(kv[1]))
			}
		}
		if rng != "" {
			p = append(p, "range="+rng)
		}
		if v.Size != defaultPageSize {
			p = append(p, "size="+strconv.Itoa(v.Size))
		}
		if v.Sort == "oldest" {
			p = append(p, "sort=oldest")
		}
		if len(p) == 0 {
			return "/memories"
		}
		return "/memories?" + strings.Join(p, "&")
	}
	anyTime := v.Range == "" && v.From == "" && v.To == ""
	v.Presets = []preset{
		{"Last 7 days", presetHref("7d"), v.Range == "7d"},
		{"Last 30 days", presetHref("30d"), v.Range == "30d"},
		{"This year", presetHref("year"), v.Range == "year"},
		{"Any time", presetHref(""), anyTime},
	}
	v.SelectPage = r.Method == http.MethodGet && q.Get("select") == "page" && len(v.Memories) > 0
	v.SelCount = len(v.Memories)
	v.PageHref = "/memories?" + v.Query
	if v.Query != "" {
		v.PageHref += "&"
	}
	v.PageHref += "page=" + strconv.Itoa(v.Page)
	v.ViewQuery = strings.TrimPrefix(v.PageHref, "/memories?")
	v.SelectHref = v.PageHref + "&select=page"
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
		CancelHref: viewURL(r),
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
	if vq := cleanView(r.PostFormValue("view")).Encode(); vq != "" {
		cv.Fields = append(cv.Fields, confirmField{"view", vq})
	}
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
	http.Redirect(w, r, viewURL(r, "batch", batch), http.StatusSeeOther)
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
	case "merge":
		return "Merged entities"
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
