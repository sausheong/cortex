package web

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/sausheong/cortex"
)

// compactKindPrefix must match jobs.CompactKindPrefix.
const compactKindPrefix = "compact:"

type compactionChange struct {
	Index   int
	Label   string
	Sources []string
	Result  string
}

type compactionView struct {
	ID      string
	Status  string
	Changes []compactionChange
	// Error is a plain-language failure; ErrLink/ErrLinkText give the next step.
	Error, ErrLink, ErrLinkText string
	ErrSelect                   string // shown next to the change list
	CanRetry                    bool   // offer "Start a new compaction"
}

func changeLabel(kind string) string {
	switch kind {
	case "merge":
		return "Merge"
	case "combine":
		return "Combine"
	case "rollup":
		return "Roll up"
	}
	return "Change"
}

func (h *compactionHandlers) view(id string, row *compactionRow) compactionView {
	v := compactionView{ID: id, Status: row.status}
	for i, c := range row.changes {
		v.Changes = append(v.Changes, compactionChange{Index: i, Label: changeLabel(c.Kind), Sources: c.Sources, Result: c.Result})
	}
	if row.status == "failed" {
		v.Error = "Compaction could not finish. This is usually the AI provider: the key may be wrong or out of credit."
		v.ErrLink, v.ErrLinkText = "/settings", "Check AI provider settings"
		v.CanRetry = true
	}
	return v
}

// CompactionMount returns the compaction routes. enqueue schedules the
// "compact:<id>" job that fills in the proposal.
func CompactionMount(enqueue func(ctx context.Context, userID, kind string) (string, error)) func(mux *http.ServeMux, d Deps) {
	return compactionMount(enqueue, nil)
}

func compactionMount(enqueue func(ctx context.Context, userID, kind string) (string, error), resolve func(ctx context.Context, userID string) (*cortex.Cortex, error)) func(mux *http.ServeMux, d Deps) {
	return func(mux *http.ServeMux, d Deps) {
		h := &compactionHandlers{d: d, enqueue: enqueue, resolve: resolve}
		mux.HandleFunc("POST /memories/compact", d.Auth(h.start))
		mux.HandleFunc("GET /compaction/{id}", d.Auth(h.show))
		mux.HandleFunc("POST /compaction/{id}/apply", d.Auth(h.apply))
		mux.HandleFunc("POST /compaction/{id}/undo", d.Auth(h.undo))
	}
}

type compactionHandlers struct {
	d       Deps
	enqueue func(ctx context.Context, userID, kind string) (string, error)
	resolve func(ctx context.Context, userID string) (*cortex.Cortex, error) // test seam
}

type compactionRow struct {
	status  string
	batchID string
	changes []cortex.Change
}

// load returns the caller's own compaction; a foreign or absent ID is
// sql.ErrNoRows.
func (h *compactionHandlers) load(ctx context.Context, userID, id string) (*compactionRow, error) {
	var row compactionRow
	var proposal, batch sql.NullString
	err := h.d.Accounts.DB.QueryRowContext(ctx,
		`SELECT status, proposal_json::text, batch_id FROM compactions WHERE id = $1 AND user_id = $2`, id, userID).
		Scan(&row.status, &proposal, &batch)
	if err != nil {
		return nil, err
	}
	row.batchID = batch.String
	if proposal.Valid && proposal.String != "" {
		if err := json.Unmarshal([]byte(proposal.String), &row.changes); err != nil {
			return nil, err
		}
	}
	return &row, nil
}

func (h *compactionHandlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	h.d.Cfg.Logger.Error("compaction", "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func (h *compactionHandlers) start(w http.ResponseWriter, r *http.Request, u SessionUser) {
	if h.enqueue == nil {
		http.Error(w, "background jobs are not available on this server", http.StatusServiceUnavailable)
		return
	}
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	id := "c_" + hex.EncodeToString(b)
	if _, err := h.d.Accounts.DB.ExecContext(r.Context(),
		`INSERT INTO compactions (id, user_id, status) VALUES ($1, $2, 'proposing')`, id, u.UserID); err != nil {
		h.fail(w, r, err)
		return
	}
	if _, err := h.enqueue(r.Context(), u.UserID, compactKindPrefix+id); err != nil {
		_, _ = h.d.Accounts.DB.ExecContext(r.Context(), `UPDATE compactions SET status = 'failed' WHERE id = $1`, id)
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/compaction/"+id, http.StatusSeeOther)
}

func (h *compactionHandlers) show(w http.ResponseWriter, r *http.Request, u SessionUser) {
	row, err := h.load(r.Context(), u.UserID, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.d.Render(w, r, "compaction", h.view(r.PathValue("id"), row))
}

// handle builds the user's own handle (with their embedder, so results are
// searchable).
func (h *compactionHandlers) handle(ctx context.Context, userID string) (*cortex.Cortex, error) {
	if h.resolve != nil {
		return h.resolve(ctx, userID)
	}
	return h.d.Accounts.ResolveUser(ctx, h.d.Root, userID, "")
}

// apply takes the selected indexes only; the changes come from the stored
// proposal, never from the request.
func (h *compactionHandlers) apply(w http.ResponseWriter, r *http.Request, u SessionUser) {
	ctx := r.Context()
	id := r.PathValue("id")
	row, err := h.load(ctx, u.UserID, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if row.status != "ready" {
		http.Error(w, "this compaction cannot be applied", http.StatusConflict)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var picked []cortex.Change
	var idx []int
	seen := map[int]bool{}
	if r.PostFormValue("select") == "all" {
		for i := range row.changes {
			seen[i] = true
			idx = append(idx, i)
		}
	}
	for _, s := range r.PostForm["idx"] {
		i, err := strconv.Atoi(s)
		if err != nil || i < 0 || i >= len(row.changes) {
			http.Error(w, "bad selection", http.StatusBadRequest)
			return
		}
		if !seen[i] {
			seen[i] = true
			idx = append(idx, i)
		}
	}
	sources := 0
	for _, i := range idx {
		picked = append(picked, row.changes[i])
		sources += len(row.changes[i].SourceIDs)
	}
	if len(picked) == 0 {
		v := h.view(id, row)
		v.ErrSelect = "Tick at least one change, or choose Review all."
		h.d.RenderStatus(w, r, http.StatusBadRequest, "compaction", v)
		return
	}
	n := len(picked)
	want, has := expectCount(r)
	if !has || want != n {
		cv := confirmView{
			Title:       "Apply " + plural(n, "change", "changes") + "?",
			Consequence: "This replaces " + plural(sources, "memory", "memories") + " with " + plural(n, "merged memory", "merged memories") + ". The originals are kept for 30 days and you can undo it afterwards.",
			Action:      "/compaction/" + id + "/apply",
			Count:       n,
			Button:      "Apply " + plural(n, "change", "changes"),
			CancelHref:  "/compaction/" + id,
		}
		for _, i := range idx {
			cv.Fields = append(cv.Fields, confirmField{"idx", strconv.Itoa(i)})
		}
		status := http.StatusOK
		if has {
			cv.Error, status = changedMsg(want, n, "changes"), http.StatusConflict
		}
		h.d.RenderStatus(w, r, status, "confirm", cv)
		return
	}
	cx, err := h.handle(ctx, u.UserID)
	if err != nil {
		v := h.view(id, row)
		v.Error, v.ErrLink, v.ErrLinkText = "cortex needs your AI provider to save merged memories, and it is not set up yet. Nothing was changed.", "/settings", "Set up your AI provider"
		h.d.RenderStatus(w, r, http.StatusConflict, "compaction", v)
		return
	}
	batch, err := cx.ApplyCompaction(ctx, picked)
	if batch != "" {
		// Applied even when a result embedding failed: record it so it can be undone.
		if _, uerr := h.d.Accounts.DB.ExecContext(ctx,
			`UPDATE compactions SET status = 'applied', batch_id = $3 WHERE id = $1 AND user_id = $2`, id, u.UserID, batch); uerr != nil {
			h.fail(w, r, uerr)
			return
		}
	}
	if err != nil {
		if errors.Is(err, cortex.ErrNotFound) {
			v := h.view(id, row)
			v.Error, v.CanRetry = "Some of these memories changed since the proposal was made. Nothing else was changed.", true
			h.d.RenderStatus(w, r, http.StatusConflict, "compaction", v)
			return
		}
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/compaction/"+id, http.StatusSeeOther)
}

func (h *compactionHandlers) undo(w http.ResponseWriter, r *http.Request, u SessionUser) {
	ctx := r.Context()
	id := r.PathValue("id")
	row, err := h.load(ctx, u.UserID, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if row.status != "applied" || row.batchID == "" {
		http.Error(w, "this compaction cannot be undone", http.StatusConflict)
		return
	}
	if err := h.d.Root.ForUser(u.UserID).UndoCompaction(ctx, row.batchID); err != nil {
		h.fail(w, r, err)
		return
	}
	if _, err := h.d.Accounts.DB.ExecContext(ctx,
		`UPDATE compactions SET status = 'undone' WHERE id = $1 AND user_id = $2`, id, u.UserID); err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/compaction/"+id, http.StatusSeeOther)
}
