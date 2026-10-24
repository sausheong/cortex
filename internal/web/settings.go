package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/keys"
)

// jobView is a background job as shown on the settings page.
type jobView struct {
	ID, Status, Error string
	Done, Total       int
	Live              bool
}

// Text is the job's progress in plain words.
func (j jobView) Text() string {
	switch j.Status {
	case "queued":
		return "Waiting to start."
	case "running":
		if j.Total > 0 {
			return fmt.Sprintf("Updating %s of %s memories. You can leave this page.", fmtInt(j.Done), fmtInt(j.Total))
		}
		return "Getting started."
	case "failed":
		return "This stopped before finishing. Check your provider settings, then update again."
	}
	return "Finished."
}

type settingsView struct {
	S     accounts.Settings // masked
	Error string
	// NotSaved is true when Error means the submitted settings were rejected
	// and nothing changed; false for follow-up errors after a successful save.
	NotSaved bool
	Saved    bool
	// ErrField names the form field Error is about ("" when it concerns the
	// whole form); the message also shows next to that field.
	ErrField string
	Job      *jobView
	Live     bool // Job is queued or running
	Pending  bool // desired embedder differs from the active one
	// MaintenanceError is the last nightly maintenance failure (fixed text), "" when clean.
	MaintenanceError string
}

func (s *server) latestReembed(ctx context.Context, userID string) (*jobView, error) {
	var j jobView
	err := s.st.DB.QueryRowContext(ctx,
		`SELECT id, status, progress_done, progress_total, error FROM jobs
		 WHERE user_id = $1 AND kind = 'reembed' ORDER BY created_at DESC, id DESC LIMIT 1`, userID).
		Scan(&j.ID, &j.Status, &j.Done, &j.Total, &j.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	j.Live = j.Status == "queued" || j.Status == "running"
	return &j, nil
}

func (s *server) embedPending(ctx context.Context, userID string, uk *keys.UK) (bool, error) {
	cur, err := s.st.GetSettings(ctx, userID, uk)
	if errors.Is(err, accounts.ErrNoSettings) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	act, err := s.st.GetActiveEmbed(ctx, userID, uk)
	if err != nil {
		return false, err
	}
	return act.EmbedModel != cur.EmbedModel || act.EmbedDims != cur.EmbedDims || act.EmbedBaseURL != cur.EffectiveEmbedURL(), nil
}

func (s *server) settingsPage(w http.ResponseWriter, r *http.Request, u SessionUser, status int, cur *accounts.Settings, msg string, saved bool, field ...string) {
	ctx := r.Context()
	v := settingsView{Error: msg, Saved: saved, NotSaved: status == http.StatusBadRequest}
	if len(field) > 0 {
		v.ErrField = field[0]
	}
	if cur == nil {
		if st, err := s.st.GetSettings(ctx, u.UserID, u.Key); err == nil {
			cur = st
		} else if !errors.Is(err, accounts.ErrNoSettings) {
			s.cfg.Logger.Error("settings", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}
	if cur == nil {
		d := accounts.Settings{}.WithDefaults()
		d.ChatModel = "gpt-4o-mini" // starting point for a first-time user
		cur = &d
	}
	v.S = cur.Masked()
	j, err := s.latestReembed(ctx, u.UserID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if j != nil && (j.Live || j.Status == "failed") {
		v.Job, v.Live = j, j.Live
	}
	var me sql.NullString
	if err := s.st.DB.QueryRowContext(ctx, `SELECT last_maintenance_error FROM users WHERE id = $1`, u.UserID).Scan(&me); err == nil {
		v.MaintenanceError = me.String
	}
	if v.Pending, err = s.embedPending(ctx, u.UserID, u.Key); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.render(w, r, status, "settings", v)
}

func (s *server) settingsGet(w http.ResponseWriter, r *http.Request, u SessionUser) {
	s.settingsPage(w, r, u, http.StatusOK, nil, "", r.URL.Query().Get("saved") == "1")
}

func (s *server) settingsPost(w http.ResponseWriter, r *http.Request, u SessionUser) {
	ctx := r.Context()
	if !s.testLimit.Allow(u.UserID) {
		http.Error(w, "too many provider tests; wait a minute", http.StatusTooManyRequests)
		return
	}
	dims, _ := strconv.Atoi(strings.TrimSpace(r.PostFormValue("embed_dims")))
	in := accounts.Settings{
		Provider:     strings.TrimSpace(r.PostFormValue("provider")),
		BaseURL:      strings.TrimSpace(r.PostFormValue("base_url")),
		APIKey:       strings.TrimSpace(r.PostFormValue("api_key")),
		ChatModel:    strings.TrimSpace(r.PostFormValue("chat_model")),
		EmbedModel:   strings.TrimSpace(r.PostFormValue("embed_model")),
		EmbedDims:    dims,
		EmbedBaseURL: strings.TrimSpace(r.PostFormValue("embed_base_url")),
		EmbedAPIKey:  strings.TrimSpace(r.PostFormValue("embed_api_key")),
	}.WithDefaults()

	prev, err := s.st.GetSettings(ctx, u.UserID, u.Key)
	first := errors.Is(err, accounts.ErrNoSettings)
	if err != nil && !first {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !first { // a blank key keeps the stored one
		if in.APIKey == "" {
			in.APIKey = prev.APIKey
		}
		if in.EmbedAPIKey == "" {
			in.EmbedAPIKey = prev.EmbedAPIKey
		}
	}
	redisplay := in // keys are masked on display
	if field, err := in.ValidateField(s.st.AllowPrivate); err != nil {
		s.settingsPage(w, r, u, http.StatusBadRequest, &redisplay, err.Error(), false, field)
		return
	}
	if err := s.testProvider(ctx, in); err != nil {
		s.settingsPage(w, r, u, http.StatusBadRequest, &redisplay, err.Error(), false)
		return
	}
	needsReembed, err := s.st.PutSettings(ctx, u.UserID, in, u.Key)
	if err != nil {
		s.settingsPage(w, r, u, http.StatusBadRequest, &redisplay, err.Error(), false)
		return
	}
	if s.opts.Enqueue != nil {
		idx, err := s.indexExists(ctx, in.EmbedDims)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if first || !idx {
			if _, err := s.opts.Enqueue(ctx, u.UserID, "index"); err != nil {
				s.cfg.Logger.Error("enqueue index", "err", err)
			}
		}
	}
	if needsReembed {
		if s.opts.Enqueue == nil {
			s.settingsPage(w, r, u, http.StatusServiceUnavailable, nil,
				"Settings were saved, but re-embedding is not available on this server.", false)
			return
		}
		if _, err := s.opts.Enqueue(ctx, u.UserID, "reembed"); err != nil {
			s.cfg.Logger.Error("enqueue reembed", "err", err)
			s.settingsPage(w, r, u, http.StatusInternalServerError, nil,
				"Settings were saved, but re-embedding could not be started. Use Update memories now.", false)
			return
		}
	}
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}

func (s *server) settingsReembed(w http.ResponseWriter, r *http.Request, u SessionUser) {
	ctx := r.Context()
	pending, err := s.embedPending(ctx, u.UserID, u.Key)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	j, err := s.latestReembed(ctx, u.UserID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if pending && (j == nil || !j.Live) {
		if s.opts.Enqueue == nil {
			s.settingsPage(w, r, u, http.StatusServiceUnavailable, nil, "Re-embedding is not available on this server.", false)
			return
		}
		if _, err := s.opts.Enqueue(ctx, u.UserID, "reembed"); err != nil {
			s.cfg.Logger.Error("enqueue reembed", "err", err)
			s.settingsPage(w, r, u, http.StatusInternalServerError, nil, "Re-embedding could not be started.", false)
			return
		}
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// jobProgress reports one of the caller's own jobs; a foreign or absent ID is 404.
func (s *server) jobProgress(w http.ResponseWriter, r *http.Request, u SessionUser) {
	var j jobView
	err := s.st.DB.QueryRowContext(r.Context(),
		`SELECT id, status, progress_done, progress_total, error FROM jobs WHERE id = $1 AND user_id = $2`,
		r.PathValue("id"), u.UserID).Scan(&j.ID, &j.Status, &j.Done, &j.Total, &j.Error)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": j.Status, "done": j.Done, "total": j.Total, "error": j.Error})
}
