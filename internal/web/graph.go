package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sausheong/cortex"
)

const (
	graphDefaultLimit = 60
	graphMaxNameLen   = 200
	graphMaxDays      = 3650
	graphMaxTypes     = 20
)

var graphLimits = []int{30, 60, 120}

func (s *server) mountGraph(mux *http.ServeMux) {
	mux.HandleFunc("GET /graph", s.auth(s.graphGet))
	mux.HandleFunc("GET /graph/data", s.graphData)
	mux.HandleFunc("POST /graph/rename", s.auth(s.graphRename))
	mux.HandleFunc("POST /graph/merge", s.auth(s.graphMerge))
	mux.HandleFunc("POST /graph/delete", s.auth(s.graphDelete))
	mux.HandleFunc("POST /graph/restore/{batch}", s.auth(s.graphRestore))
}

// Shapes are drawn in a 22 by 22 box around the origin. They are the one
// source for both the canvas (sent in the JSON) and the legend, so a type is
// always recognisable by shape as well as by its label.
var shapePaths = map[string]string{
	"circle":   "M0-10A10 10 0 1 1 0 10A10 10 0 1 1 0-10Z",
	"square":   "M-9-9H9V9H-9Z",
	"diamond":  "M0-11L11 0L0 11L-11 0Z",
	"triangle": "M0-11L10 8H-10Z",
	"hexagon":  "M-5.5-9.5H5.5L11 0L5.5 9.5H-5.5L-11 0Z",
	"pill":     "M-5-6H5A6 6 0 0 1 5 6H-5A6 6 0 0 1-5-6Z",
	"ring":     "M0-10A10 10 0 1 1 0 10A10 10 0 1 1 0-10ZM0-5A5 5 0 1 0 0 5A5 5 0 1 0 0-5Z",
}

func shapeFor(typ string) string {
	switch strings.ToLower(typ) {
	case "person":
		return "circle"
	case "organization", "organisation", "company":
		return "square"
	case "project":
		return "diamond"
	case "concept", "topic":
		return "hexagon"
	case "event":
		return "triangle"
	case "document":
		return "pill"
	}
	return "ring"
}

// typeLabel turns an entity type into a display word ("work_item" -> "Work item").
func typeLabel(t string) string {
	t = strings.ReplaceAll(strings.TrimSpace(t), "_", " ")
	if t == "" {
		return "Other"
	}
	r, n := utf8.DecodeRuneInString(t)
	return string(unicode.ToUpper(r)) + t[n:]
}

// relLabel turns a relationship type into words ("works_at" -> "works at").
func relLabel(t string) string {
	t = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(t), "_", " "))
	if t == "" {
		return "is related to"
	}
	return t
}

// graphParams are the view controls shared by the page and the data endpoint.
type graphParams struct {
	Center string
	Limit  int
	Types  []string
	Days   int
}

func parseGraphParams(q url.Values) graphParams {
	p := graphParams{Center: strings.TrimSpace(q.Get("center")), Limit: graphDefaultLimit}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil {
		for _, l := range graphLimits {
			if n == l {
				p.Limit = n
			}
		}
	}
	if n, err := strconv.Atoi(q.Get("days")); err == nil && n > 0 {
		p.Days = min(n, graphMaxDays)
	}
	seen := map[string]bool{}
	for _, v := range q["types"] {
		for _, t := range strings.Split(v, ",") {
			t = strings.TrimSpace(t)
			if t != "" && !seen[t] && len(p.Types) < graphMaxTypes && utf8.RuneCountInString(t) <= 100 {
				seen[t] = true
				p.Types = append(p.Types, t)
			}
		}
	}
	return p
}

// values encodes the filters (not the centre) for carrying through links.
func (p graphParams) values() url.Values {
	v := url.Values{}
	if p.Limit != graphDefaultLimit {
		v.Set("limit", strconv.Itoa(p.Limit))
	}
	if len(p.Types) > 0 {
		v.Set("types", strings.Join(p.Types, ","))
	}
	if p.Days > 0 {
		v.Set("days", strconv.Itoa(p.Days))
	}
	return v
}

func (p graphParams) href(center string) string {
	v := p.values()
	v.Set("center", center)
	return "/graph?" + v.Encode()
}

func (p graphParams) filter(now time.Time) cortex.GraphFilter {
	f := cortex.GraphFilter{Types: p.Types, Limit: p.Limit}
	if p.Days > 0 {
		t := now.AddDate(0, 0, -p.Days)
		f.Since = &t
	}
	return f
}

// resolveCenter returns the centre to draw: the requested one, else the
// user's own entity. ok is false when there is none to draw.
func resolveCenter(r *http.Request, cx *cortex.Cortex, want string) (id string, ok bool) {
	if want != "" {
		return want, true
	}
	id, err := cx.OwnerEntityID(r.Context())
	return id, err == nil
}

// ---- JSON endpoint ----

type nodeJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	TypeLabel string `json:"type_label"`
	Shape     string `json:"shape"`
	Path      string `json:"path"`
	Center    bool   `json:"center"`
	Rels      int    `json:"rels"`
}

type edgeJSON struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Target string `json:"target"`
	Type   string `json:"type"`
	Label  string `json:"label"`
}

type graphJSON struct {
	Center    string     `json:"center"`
	Nodes     []nodeJSON `json:"nodes"`
	Edges     []edgeJSON `json:"edges"`
	Truncated bool       `json:"truncated"`
	Limit     int        `json:"limit"`
}

func toJSON(g *cortex.Neighbourhood, center string, limit int) graphJSON {
	out := graphJSON{Center: center, Nodes: []nodeJSON{}, Edges: []edgeJSON{}, Truncated: g.Truncated, Limit: limit}
	for _, n := range g.Nodes {
		sh := shapeFor(n.Type)
		out.Nodes = append(out.Nodes, nodeJSON{ID: n.ID, Name: n.Name, Type: n.Type, TypeLabel: typeLabel(n.Type),
			Shape: sh, Path: shapePaths[sh], Center: n.Center, Rels: n.Rels})
	}
	for _, e := range g.Edges {
		out.Edges = append(out.Edges, edgeJSON{ID: e.ID, Source: e.SourceID, Target: e.TargetID, Type: e.Type, Label: relLabel(e.Type)})
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	// Per-user data: never stored by a browser cache or a shared cache.
	h.Set("Cache-Control", "no-store")
	h.Set("Vary", "Cookie")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// graphData serves the neighbourhood of an entity as JSON for the signed-in
// user only. A centre that is absent or belongs to someone else is 404.
func (s *server) graphData(w http.ResponseWriter, r *http.Request) {
	u, ok := s.sessionFor(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in required"})
		return
	}
	cx := s.root.ForUser(u.UserID)
	p := parseGraphParams(r.URL.Query())
	center, ok := resolveCenter(r, cx, p.Center)
	if !ok {
		writeJSON(w, http.StatusOK, graphJSON{Nodes: []nodeJSON{}, Edges: []edgeJSON{}, Limit: p.Limit})
		return
	}
	g, err := cx.GraphNeighbourhood(r.Context(), center, p.filter(s.now()))
	if errors.Is(err, cortex.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if err != nil {
		s.cfg.Logger.Error("graph data", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, toJSON(g, center, p.Limit))
}

// ---- Page ----

type kv struct{ K, V string }

type relRow struct {
	Label     string
	Outgoing  bool
	OtherID   string
	OtherName string
	OtherType string
	Href      string
}

type memRow struct {
	Text, Ago, Abs, ISO string
}

type panelView struct {
	ID, Name, Type, TypeLabel string
	Shape                     template.HTML // inline svg of the type shape
	Attrs                     []kv
	Mentions                  int
	MemHref                   string
	Memories                  []memRow
	Relations                 []relRow
	RelTotal                  int
	IsOwner                   bool
}

type nodeRow struct {
	ID, Name, TypeLabel string
	Shape               template.HTML
	Rels                int
	Href                string
}

type typeOpt struct {
	Value, Label string
	Shape        template.HTML
	On           bool
}

type searchRow struct {
	Name, TypeLabel string
	Shape           template.HTML
	Href            string
}

type mergeCand struct {
	ID, Name string
}

type mergeOffer struct {
	TargetID, TargetName, Name string
}

type graphUndo struct {
	BatchID  string
	Kind     string // "merge" or "entity"
	Rel      int
	Target   string // merge: name of the entity kept
	EntityID string // where Undo returns to
}

type graphView struct {
	Q                string
	Searched         bool
	Results          []searchRow
	Panel            *panelView
	Nodes            []nodeRow
	NodeTotal        int
	Truncated        bool
	Types            []typeOpt
	Legend           []typeOpt
	Days             int
	Limit            int
	Limits           []int
	DayChoices       []int
	DataURL          string
	CenterID         string
	Filtered         bool
	Empty            bool // the user has no entities at all
	Error, ErrField  string
	RenameVal        string
	Offer            *mergeOffer
	MergeQ           string
	MergeSearched    bool
	MergeResults     []mergeCand
	Undo             *graphUndo
	Restored         string // "merge" or "entity" after an Undo
	Renamed          bool
	ClearFiltersHref string
}

func svgShape(shape string) template.HTML {
	d, ok := shapePaths[shape]
	if !ok {
		d = shapePaths["ring"]
	}
	return template.HTML(`<svg class="shape" aria-hidden="true" viewBox="-12 -12 24 24" width="16" height="16"><path d="` + d + `" fill-rule="evenodd"/></svg>`)
}

type graphOpts struct {
	status    int
	err       string
	errField  string
	renameVal string
	offer     *mergeOffer
}

func (s *server) graphGet(w http.ResponseWriter, r *http.Request, u SessionUser) {
	s.graphPage(w, r, u, parseGraphParams(r.URL.Query()), r.URL.Query(), graphOpts{status: http.StatusOK})
}

// graphPage builds and renders the page for p. q holds the other query
// values: q (search), mergeq, batch, restored, renamed.
func (s *server) graphPage(w http.ResponseWriter, r *http.Request, u SessionUser, p graphParams, q url.Values, o graphOpts) {
	ctx := r.Context()
	cx := s.root.ForUser(u.UserID)
	v := graphView{Q: strings.TrimSpace(q.Get("q")), Days: p.Days, Limit: p.Limit, Limits: graphLimits, DayChoices: []int{7, 30, 90, 365},
		Error: o.err, ErrField: o.errField, RenameVal: o.renameVal, Offer: o.offer, Restored: q.Get("restored"), Renamed: q.Get("renamed") == "1"}
	status := o.status
	fail := func(err error) {
		s.cfg.Logger.Error("graph", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}

	owner, ownerErr := cx.OwnerEntityID(ctx)
	center, ok := resolveCenter(r, cx, p.Center)
	var g *cortex.Neighbourhood
	if ok {
		var err error
		g, err = cx.GraphNeighbourhood(ctx, center, p.filter(s.now()))
		if errors.Is(err, cortex.ErrNotFound) {
			// An unknown or foreign ID is shown exactly like an absent one.
			status = http.StatusNotFound
			v.Error, v.ErrField = "That item is not in your graph.", ""
			p.Center = ""
			center, ok = resolveCenter(r, cx, "")
			g = nil
			if ok {
				g, err = cx.GraphNeighbourhood(ctx, center, p.filter(s.now()))
			}
		}
		if err != nil && !errors.Is(err, cortex.ErrNotFound) {
			fail(err)
			return
		}
	}
	if ownerErr != nil {
		owner = ""
	}

	types, err := cx.EntityTypes(ctx)
	if err != nil {
		fail(err)
		return
	}
	on := map[string]bool{}
	for _, t := range p.Types {
		on[t] = true
	}
	for _, t := range types {
		v.Types = append(v.Types, typeOpt{Value: t, Label: typeLabel(t), Shape: svgShape(shapeFor(t)), On: len(on) == 0 || on[t]})
	}
	v.Filtered = len(p.Types) > 0 || p.Days > 0
	v.ClearFiltersHref = "/graph"
	v.Empty = len(types) == 0

	if ok && g != nil {
		v.CenterID = center
		dv := p.values()
		dv.Set("center", center)
		v.DataURL = "/graph/data?" + dv.Encode()
		v.Truncated = g.Truncated
		v.NodeTotal = len(g.Nodes)
		legend := map[string]bool{}
		for _, n := range g.Nodes {
			if !legend[n.Type] {
				legend[n.Type] = true
				v.Legend = append(v.Legend, typeOpt{Value: n.Type, Label: typeLabel(n.Type), Shape: svgShape(shapeFor(n.Type))})
			}
			if n.Center {
				continue
			}
			v.Nodes = append(v.Nodes, nodeRow{ID: n.ID, Name: n.Name, TypeLabel: typeLabel(n.Type), Shape: svgShape(shapeFor(n.Type)), Rels: n.Rels, Href: p.href(n.ID)})
		}
		panel, err := s.buildPanel(r, cx, p, center, owner)
		if err != nil {
			fail(err)
			return
		}
		v.Panel = panel
		if q.Get("mergeq") != "" {
			v.MergeQ = strings.TrimSpace(q.Get("mergeq"))
			v.MergeSearched = v.MergeQ != ""
			ents, err := cx.SearchEntities(ctx, v.MergeQ, []string{panel.Type}, center, 10)
			if err != nil {
				fail(err)
				return
			}
			for _, e := range ents {
				v.MergeResults = append(v.MergeResults, mergeCand{ID: e.ID, Name: e.Name})
			}
		}
	}

	if v.Q != "" {
		v.Searched = true
		ents, err := cx.SearchEntities(ctx, v.Q, nil, "", 20)
		if err != nil {
			fail(err)
			return
		}
		for _, e := range ents {
			v.Results = append(v.Results, searchRow{Name: e.Name, TypeLabel: typeLabel(e.Type), Shape: svgShape(shapeFor(e.Type)), Href: p.href(e.ID)})
		}
	}

	// The undo notice is built from the stored batch, never from the URL, so a
	// crafted link cannot show a false count.
	if b := q.Get("batch"); b != "" {
		if bs, err := cx.ListDeleteBatches(ctx); err == nil {
			for _, d := range bs {
				if d.ID != b || (d.Kind != "merge" && d.Kind != "entity") {
					continue
				}
				un := &graphUndo{BatchID: d.ID, Kind: d.Kind, Rel: d.Relationships, EntityID: q.Get("entity")}
				if d.Kind == "merge" && v.Panel != nil {
					un.Target = v.Panel.Name
				}
				v.Undo = un
			}
		}
	}
	if p.Days > 0 {
		known := false
		for _, d := range v.DayChoices {
			known = known || d == p.Days
		}
		if !known {
			v.DayChoices = append(v.DayChoices, p.Days)
			sort.Ints(v.DayChoices)
		}
	}
	s.render(w, r, status, "graph", v)
}

func snippet(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	rs := []rune(s)
	return strings.TrimSpace(string(rs[:n])) + "…"
}

// attrVisible hides bookkeeping attributes (merge history, samples, ids,
// underscore-prefixed keys) and structured values that read as raw JSON.
func attrVisible(k string, v any) bool {
	lk := strings.ToLower(k)
	switch lk {
	case "merged_from", "sample", "samples", "source", "sources", "embedding", "id", "ids":
		return false
	}
	if strings.HasPrefix(lk, "_") || strings.HasPrefix(lk, "sample") || strings.HasSuffix(lk, "_id") || strings.HasSuffix(lk, "_ids") {
		return false
	}
	switch v.(type) {
	case map[string]any, []any:
		return false
	}
	return true
}

// attrLabel turns a key like "birth_date" into "Birth date".
func attrLabel(k string) string {
	s := strings.TrimSpace(strings.ReplaceAll(k, "_", " "))
	if s == "" {
		return k
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func attrValue(v any) string {
	switch x := v.(type) {
	case string:
		return snippet(x, 160)
	case nil:
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return snippet(fmt.Sprint(v), 160)
	}
	return snippet(string(b), 160)
}

func (s *server) buildPanel(r *http.Request, cx *cortex.Cortex, p graphParams, id, owner string) (*panelView, error) {
	ctx := r.Context()
	e, err := cx.GetEntity(ctx, id)
	if err != nil {
		return nil, err
	}
	pv := &panelView{ID: e.ID, Name: e.Name, Type: e.Type, TypeLabel: typeLabel(e.Type), Shape: svgShape(shapeFor(e.Type)),
		IsOwner: id == owner, MemHref: "/memories?entity=" + url.QueryEscape(e.ID)}
	keys := make([]string, 0, len(e.Attributes))
	for k := range e.Attributes {
		if attrVisible(k, e.Attributes[k]) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if val := attrValue(e.Attributes[k]); val != "" {
			pv.Attrs = append(pv.Attrs, kv{attrLabel(k), val})
		}
	}
	rels, total, err := cx.EntityRelations(ctx, id, 50)
	if err != nil {
		return nil, err
	}
	pv.RelTotal = total
	for _, rl := range rels {
		pv.Relations = append(pv.Relations, relRow{Label: relLabel(rl.Type), Outgoing: rl.Outgoing, OtherID: rl.OtherID,
			OtherName: rl.OtherName, OtherType: typeLabel(rl.OtherType), Href: p.href(rl.OtherID)})
	}
	mems, n, err := cx.EntityMentions(ctx, id, 10)
	if err != nil {
		return nil, err
	}
	pv.Mentions = n
	now := s.now()
	for _, m := range mems {
		pv.Memories = append(pv.Memories, memRow{Text: snippet(m.Content, 200), Ago: relTime(now, m.CreatedAt), Abs: absTime(m.CreatedAt), ISO: m.CreatedAt.UTC().Format(time.RFC3339)})
	}
	return pv, nil
}

// ---- Mutations ----

func (s *server) graphErr(w http.ResponseWriter, r *http.Request, u SessionUser, status int, center, field, msg string) {
	s.graphPage(w, r, u, graphParams{Center: center, Limit: graphDefaultLimit}, url.Values{}, graphOpts{status: status, err: msg, errField: field})
}

// ownerOf reports whether id is the user's own entity.
func ownerOf(r *http.Request, cx *cortex.Cortex, id string) bool {
	o, err := cx.OwnerEntityID(r.Context())
	return err == nil && o == id
}

func validName(name string) string {
	switch {
	case name == "":
		return "Enter a name."
	case utf8.RuneCountInString(name) > graphMaxNameLen:
		return "Use " + strconv.Itoa(graphMaxNameLen) + " characters or fewer."
	}
	for _, c := range name {
		if unicode.IsControl(c) {
			return "Use plain text with no line breaks."
		}
	}
	return ""
}

func (s *server) graphRename(w http.ResponseWriter, r *http.Request, u SessionUser) {
	ctx := r.Context()
	cx := s.root.ForUser(u.UserID)
	id := strings.TrimSpace(r.PostFormValue("id"))
	name := strings.TrimSpace(r.PostFormValue("name"))
	ent, err := cx.GetEntity(ctx, id)
	if errors.Is(err, cortex.ErrNotFound) {
		s.graphErr(w, r, u, http.StatusNotFound, "", "", "That item is not in your graph.")
		return
	}
	if err != nil {
		s.cfg.Logger.Error("graph rename", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	redo := func(status int, msg string, offer *mergeOffer) {
		s.graphPage(w, r, u, graphParams{Center: id, Limit: graphDefaultLimit}, url.Values{},
			graphOpts{status: status, err: msg, errField: "rename", renameVal: name, offer: offer})
	}
	if msg := validName(name); msg != "" {
		redo(http.StatusBadRequest, msg, nil)
		return
	}
	if name == ent.Name {
		redo(http.StatusBadRequest, "That is already its name.", nil)
		return
	}
	err = cx.RenameEntity(ctx, id, name)
	var taken *cortex.ErrEntityNameTaken
	if errors.As(err, &taken) {
		msg := "Another " + strings.ToLower(typeLabel(ent.Type)) + " is already called " + name + "."
		if ownerOf(r, cx, id) {
			redo(http.StatusConflict, msg+" You cannot be merged into another item, so choose a different name.", nil)
			return
		}
		redo(http.StatusConflict, msg, &mergeOffer{TargetID: taken.ConflictID, TargetName: name, Name: ent.Name})
		return
	}
	if err != nil {
		s.cfg.Logger.Error("graph rename", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/graph?center="+url.QueryEscape(id)+"&renamed=1", http.StatusSeeOther)
}

func (s *server) graphMerge(w http.ResponseWriter, r *http.Request, u SessionUser) {
	ctx := r.Context()
	cx := s.root.ForUser(u.UserID)
	keepID := strings.TrimSpace(r.PostFormValue("keep"))
	dropID := strings.TrimSpace(r.PostFormValue("drop"))
	if keepID == dropID {
		s.graphErr(w, r, u, http.StatusBadRequest, dropID, "merge", "Choose a different item to merge into.")
		return
	}
	keep, kerr := cx.GetEntity(ctx, keepID)
	drop, derr := cx.GetEntity(ctx, dropID)
	if kerr != nil || derr != nil {
		if (kerr != nil && !errors.Is(kerr, cortex.ErrNotFound)) || (derr != nil && !errors.Is(derr, cortex.ErrNotFound)) {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		s.graphErr(w, r, u, http.StatusNotFound, "", "", "That item is not in your graph.")
		return
	}
	if ownerOf(r, cx, dropID) {
		s.graphErr(w, r, u, http.StatusForbidden, dropID, "merge", "This item is you, so it cannot be merged into another one. Merge the other into this one instead.")
		return
	}
	if keep.Type != drop.Type {
		s.graphErr(w, r, u, http.StatusBadRequest, dropID, "merge", "Only items of the same type can be merged.")
		return
	}
	st, err := cx.MergeEntitiesDryRun(ctx, keepID, dropID)
	if err != nil {
		s.cfg.Logger.Error("graph merge preview", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	n := st.Relationships + st.Memories + st.Chunks + st.DupesDropped
	cv := confirmView{
		Title:       "Merge " + drop.Name + " into " + keep.Name + "?",
		Consequence: drop.Name + " is removed and everything linked to it moves to " + keep.Name + ". You can undo this for 30 days.",
		Details: []string{
			"Moves " + plural(st.Relationships, "relationship", "relationships") + ", " + plural(st.Memories, "memory link", "memory links") + " and " + plural(st.Chunks, "chunk", "chunks") + ". " +
				plural(st.DupesDropped, "duplicate is", "duplicates are") + " dropped.",
			"Undo right after, or from Recently deleted. After 30 days the merge is permanent.",
		},
		Action:     "/graph/merge",
		Fields:     []confirmField{{"keep", keepID}, {"drop", dropID}},
		Count:      n,
		Button:     "Merge " + drop.Name,
		CancelHref: "/graph?center=" + url.QueryEscape(dropID),
	}
	want, has := expectCount(r)
	if !has {
		s.renderConfirm(w, r, http.StatusOK, cv)
		return
	}
	if want != n {
		cv.Error = changedMsg(want, n, "items to move")
		s.renderConfirm(w, r, http.StatusConflict, cv)
		return
	}
	res, err := cx.MergeEntities(ctx, keepID, dropID)
	if err != nil {
		s.cfg.Logger.Error("graph merge", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/graph?center="+url.QueryEscape(keepID)+"&batch="+url.QueryEscape(res.BatchID)+"&entity="+url.QueryEscape(dropID), http.StatusSeeOther)
}

func (s *server) graphDelete(w http.ResponseWriter, r *http.Request, u SessionUser) {
	ctx := r.Context()
	cx := s.root.ForUser(u.UserID)
	id := strings.TrimSpace(r.PostFormValue("id"))
	ent, err := cx.GetEntity(ctx, id)
	if errors.Is(err, cortex.ErrNotFound) {
		s.graphErr(w, r, u, http.StatusNotFound, "", "", "That item is not in your graph.")
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if ownerOf(r, cx, id) {
		s.graphErr(w, r, u, http.StatusForbidden, id, "delete", "This item is you, so it cannot be deleted.")
		return
	}
	_, n, err := cx.EntityRelations(ctx, id, 1)
	if err != nil {
		s.cfg.Logger.Error("graph delete count", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	cv := confirmView{
		Title:       "Delete " + ent.Name + "?",
		Consequence: "This removes " + ent.Name + " and its " + plural(n, "relationship", "relationships") + " from your graph. Memories that mention it are kept. You can restore it for 30 days.",
		Details:     []string{"Undo right after, or restore it from Recently deleted. After 30 days it is removed for good."},
		Action:      "/graph/delete",
		Fields:      []confirmField{{"id", id}},
		Count:       n,
		Button:      "Delete " + ent.Name,
		CancelHref:  "/graph?center=" + url.QueryEscape(id),
	}
	want, has := expectCount(r)
	if !has {
		s.renderConfirm(w, r, http.StatusOK, cv)
		return
	}
	if want != n {
		cv.Error = changedMsg(want, n, "relationships")
		s.renderConfirm(w, r, http.StatusConflict, cv)
		return
	}
	batch, err := cx.SoftDeleteEntity(ctx, id, "web:"+u.UserID)
	if errors.Is(err, cortex.ErrNotFound) {
		s.graphErr(w, r, u, http.StatusNotFound, "", "", "That item is already gone, so nothing was deleted.")
		return
	}
	if err != nil {
		s.cfg.Logger.Error("graph delete", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/graph?batch="+url.QueryEscape(batch)+"&entity="+url.QueryEscape(id), http.StatusSeeOther)
}

func (s *server) graphRestore(w http.ResponseWriter, r *http.Request, u SessionUser) {
	ctx := r.Context()
	cx := s.root.ForUser(u.UserID)
	id := r.PathValue("batch")
	kind := ""
	if bs, err := cx.ListDeleteBatches(ctx); err == nil {
		for _, b := range bs {
			if b.ID == id && (b.Kind == "merge" || b.Kind == "entity") {
				kind = b.Kind
			}
		}
	}
	if kind == "" {
		http.NotFound(w, r)
		return
	}
	err := cx.RestoreBatch(ctx, id)
	if errors.Is(err, cortex.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.cfg.Logger.Error("graph restore", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	dest := "/graph?restored=" + kind
	// Return to the entity that was removed if it is back and is the user's.
	if e := strings.TrimSpace(r.PostFormValue("entity")); e != "" {
		if _, err := cx.GetEntity(ctx, e); err == nil {
			dest = "/graph?center=" + url.QueryEscape(e) + "&restored=" + kind
		}
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}
