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
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/vaultart"
)

// vaultView is the Vault section of the settings page. It has its own form
// and POST so saving the provider settings never touches it.
type vaultView struct {
	Threshold        string
	IncludeConnected bool
	Length           string
	Error            string // threshold error, shown next to the field
	NotSaved         bool
	Saved            bool
	// Usage is the plain-words usage line, "" when nothing has been written.
	Usage       string
	CannotWrite bool
}

// vaultUsage words the usage line: the article count, and the tokens and
// failures of the last 10 attempts when there are any.
func vaultUsage(articles int, u vaultart.Usage) string {
	if articles == 0 && u.Failed == 0 && u.Tokens == 0 {
		return ""
	}
	noun := "articles"
	if articles == 1 {
		noun = "article"
	}
	out := fmt.Sprintf("%s %s written", fmtInt(articles), noun)
	if u.Tokens > 0 {
		out += fmt.Sprintf(", about %s tokens in the last 10 attempts", fmtInt(u.Tokens))
	}
	if u.Failed > 0 {
		out += fmt.Sprintf(", %s failed", fmtInt(u.Failed))
	}
	return out + "."
}

// loadVaultView builds the section. A nil result hides it (no key to open the
// settings with).
func (s *server) loadVaultView(ctx context.Context, u SessionUser, form *vaultView) (*vaultView, error) {
	cx, release := cxFor(ctx, s.root, s.st, u)
	defer release()
	h := cx.StoreHandle()
	cur, err := vaultart.LoadSettings(ctx, h)
	if errors.Is(err, model.ErrLocked) || errors.Is(err, model.ErrEnrollRequired) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v := &vaultView{Threshold: strconv.Itoa(cur.Threshold), IncludeConnected: cur.IncludeConnected, Length: cur.Length}
	if form != nil { // redisplay what was submitted
		v.Threshold, v.IncludeConnected, v.Length = form.Threshold, form.IncludeConnected, form.Length
		v.Error, v.NotSaved = form.Error, form.NotSaved
	}
	v.CannotWrite = !vaultart.CanWrite(cx)
	if rows, err := h.ListArticleMeta(ctx); err == nil {
		if us, err := s.art.RecentUsageLast(ctx, cx, 10); err == nil {
			v.Usage = vaultUsage(len(rows), us)
		}
	}
	return v, nil
}

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
	Vault            *vaultView
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
	s.settingsPageV(w, r, u, status, cur, msg, saved, nil, field...)
}

func (s *server) settingsPageV(w http.ResponseWriter, r *http.Request, u SessionUser, status int, cur *accounts.Settings, msg string, saved bool, vform *vaultView, field ...string) {
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
	vv, err := s.loadVaultView(ctx, u, vform)
	if err != nil {
		s.cfg.Logger.Error("settings vault", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if vv != nil {
		vv.Saved = r.URL.Query().Get("vault") == "saved" && vform == nil
	}
	v.Vault = vv
	s.render(w, r, status, "settings", v)
}

// settingsVaultPost saves the Vault section on its own, apart from the
// provider form.
func (s *server) settingsVaultPost(w http.ResponseWriter, r *http.Request, u SessionUser) {
	ctx := r.Context()
	form := &vaultView{
		Threshold:        strings.TrimSpace(r.PostFormValue("threshold")),
		IncludeConnected: r.PostFormValue("include_connected") != "",
		Length:           r.PostFormValue("length"),
		NotSaved:         true,
	}
	n, err := strconv.Atoi(form.Threshold)
	if err != nil || n < vaultart.MinThreshold || n > vaultart.MaxThreshold {
		form.Error = fmt.Sprintf("Enter a whole number from %d to %d.", vaultart.MinThreshold, vaultart.MaxThreshold)
		s.settingsPageV(w, r, u, http.StatusBadRequest, nil, "", false, form)
		return
	}
	switch form.Length {
	case vaultart.LengthShort, vaultart.LengthStandard, vaultart.LengthLong:
	default:
		form.Length = vaultart.LengthStandard
	}
	cx, release := cxFor(ctx, s.root, s.st, u)
	defer release()
	err = vaultart.SaveSettings(ctx, cx.StoreHandle(), vaultart.Settings{Threshold: n, IncludeConnected: form.IncludeConnected, Length: form.Length})
	if errors.Is(err, model.ErrLocked) || errors.Is(err, model.ErrEnrollRequired) {
		s.settingsPage(w, r, u, http.StatusForbidden, nil, enrollRequiredMsg, false)
		return
	}
	if err != nil {
		s.cfg.Logger.Error("save vault settings", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings?vault=saved#vault", http.StatusSeeOther)
}

func (s *server) settingsGet(w http.ResponseWriter, r *http.Request, u SessionUser) {
	s.settingsPage(w, r, u, http.StatusOK, nil, "", r.URL.Query().Get("saved") == "1")
}

func (s *server) settingsPost(w http.ResponseWriter, r *http.Request, u SessionUser) {
	ctx := r.Context()
	if req, err := s.st.EnrollRequired(ctx, u.UserID); err == nil && req {
		s.settingsPage(w, r, u, http.StatusForbidden, nil, enrollRequiredMsg, false)
		return
	}
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
	if errors.Is(err, model.ErrEnrollRequired) {
		s.settingsPage(w, r, u, http.StatusForbidden, nil, enrollRequiredMsg, false)
		return
	}
	if err != nil {
		s.settingsPage(w, r, u, http.StatusBadRequest, &redisplay, err.Error(), false)
		return
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
