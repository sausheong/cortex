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

type compactionView struct {
	ID      string
	Status  string
	Changes []cortex.Change
	Error   string
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
	h.d.Render(w, r, "compaction", compactionView{ID: r.PathValue("id"), Status: row.status, Changes: row.changes})
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
	seen := map[int]bool{}
	for _, s := range r.PostForm["idx"] {
		i, err := strconv.Atoi(s)
		if err != nil || i < 0 || i >= len(row.changes) {
			http.Error(w, "bad selection", http.StatusBadRequest)
			return
		}
		if !seen[i] {
			seen[i] = true
			picked = append(picked, row.changes[i])
		}
	}
	if len(picked) == 0 {
		http.Redirect(w, r, "/compaction/"+id, http.StatusSeeOther)
		return
	}
	cx, err := h.handle(ctx, u.UserID)
	if err != nil {
		http.Error(w, "set up your AI provider first", http.StatusConflict)
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
			http.Error(w, "some memories changed since the proposal; start a new compaction", http.StatusConflict)
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
