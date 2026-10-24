package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/keys"
)

// Passkey enrollment and unlock endpoints.
//
// Every route is behind s.auth, so a live session is required and every POST
// carries the session's CSRF token (X-CSRF-Token for the JSON calls). The
// PRF output arrives over TLS in a request body and is handed straight to the
// accounts layer; it is never logged, stored or echoed. The only logging in
// this file is a fixed step name on an internal error: never a request body,
// query string (including next=), header or error text from the WebAuthn
// library.

const maxPasskeyBody = 256 << 10

func (s *server) mountPasskeys(mux *http.ServeMux) {
	mux.HandleFunc("GET /enroll", s.auth(s.enrollPage))
	mux.HandleFunc("POST /enroll/begin", s.auth(s.enrollBegin))
	mux.HandleFunc("POST /enroll/finish", s.auth(s.enrollFinish))
	mux.HandleFunc("POST /enroll/confirm/begin", s.auth(s.confirmBegin))
	mux.HandleFunc("POST /enroll/confirm", s.auth(s.confirmFinish))
	mux.HandleFunc("GET /unlock", s.auth(s.unlockPage))
	mux.HandleFunc("POST /unlock/begin", s.auth(s.unlockBegin))
	mux.HandleFunc("POST /unlock/finish", s.auth(s.unlockFinish))
	mux.HandleFunc("POST /unlock/recovery", s.auth(s.unlockRecovery))
}

// configurePasskeys derives the relying party from PublicURL: the RP ID is
// its host without the port (fixed to cortex.sausheong.dev in production) and
// the origin is its scheme and host with port.
func (s *server) configurePasskeys() {
	u, err := url.Parse(s.cfg.PublicURL)
	if err != nil || u.Hostname() == "" {
		return
	}
	if err := s.st.ConfigurePasskeys(accounts.PasskeyConfig{
		RPID: u.Hostname(), RPName: "cortex",
		Origins:       []string{u.Scheme + "://" + u.Host},
		EnrollEnabled: s.cfg.EncryptionEnroll,
	}); err != nil {
		s.cfg.Logger.Error("passkeys", "step", "configure")
	}
}

// safeNext returns next only when it is a same-origin relative path: one
// leading slash, not "//" or "/\", no scheme or host, no control characters.
// Anything else becomes "/".
func safeNext(next string) string {
	if next == "" || len(next) > 2048 || next[0] != '/' {
		return "/"
	}
	if len(next) > 1 && (next[1] == '/' || next[1] == '\\') {
		return "/"
	}
	for _, c := range next {
		if c < 0x20 || c == 0x7f || c == '\\' {
			return "/"
		}
	}
	if u, err := url.Parse(next); err != nil || u.Scheme != "" || u.Host != "" || u.Opaque != "" {
		return "/"
	}
	return next
}

// encState reads the user's encryption_state.
func (s *server) encState(ctx context.Context, userID string) (string, error) {
	var st string
	err := s.st.DB.QueryRowContext(ctx, `SELECT encryption_state FROM users WHERE id = $1`, userID).Scan(&st)
	return st, err
}

// authUnlocked is auth plus the lock gate. For an encrypted account without a
// key it shows the unlock page (GET) or answers 423 (anything else), and the
// handler never runs. A legacy ('none') account passes straight through.
func (s *server) authUnlocked(next func(w http.ResponseWriter, r *http.Request, u SessionUser)) http.HandlerFunc {
	return s.auth(func(w http.ResponseWriter, r *http.Request, u SessionUser) {
		st, err := s.encState(r.Context(), u.UserID)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if st != "none" && !u.Unlocked {
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				s.render(w, r, http.StatusOK, "unlock", unlockData{Next: safeNext(r.URL.RequestURI())})
				return
			}
			http.Error(w, "locked: unlock with your passkey", http.StatusLocked)
			return
		}
		next(w, r, u)
	})
}

// --- helpers ---

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxPasskeyBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "That request was not understood."})
		return false
	}
	return true
}

func decodePRF(s string) []byte {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		return nil
	}
	return b
}

// pkFail maps an accounts error to a fixed-text JSON answer.
func (s *server) pkFail(w http.ResponseWriter, step string, err error) {
	status, msg := http.StatusInternalServerError, "Something went wrong. Try again."
	switch {
	case errors.Is(err, accounts.ErrEnrollDisabled):
		status, msg = http.StatusNotFound, "Passkey protection is not available yet."
	case errors.Is(err, accounts.ErrNoPRF):
		status, msg = http.StatusBadRequest, accounts.ErrNoPRF.Error()+"."
	case errors.Is(err, accounts.ErrCeremony):
		status, msg = http.StatusBadRequest, "That step expired. Start again."
	case errors.Is(err, accounts.ErrAlreadyEnrolled), errors.Is(err, accounts.ErrNotEnrolled):
		status, msg = http.StatusConflict, err.Error()+"."
	case errors.Is(err, accounts.ErrProof):
		status, msg = http.StatusUnauthorized, "The passkey or the recovery key groups did not match. Check them and try again."
	case errors.Is(err, accounts.ErrPasskeyVerify), errors.Is(err, accounts.ErrSignCount), errors.Is(err, keys.ErrDecrypt):
		status, msg = http.StatusUnauthorized, "That passkey could not unlock your memory."
	case errors.Is(err, accounts.ErrBadRecovery):
		status, msg = http.StatusUnauthorized, "That recovery key did not match."
	default:
		s.cfg.Logger.Error("passkey", "step", step)
	}
	writeJSON(w, status, map[string]string{"error": msg})
}

// allowUnlock applies the per-IP and per-user throttles. It answers 429 and
// reports false when either is spent.
func (s *server) allowUnlock(w http.ResponseWriter, r *http.Request, userID string, extra *accounts.Limiter) bool {
	if !s.unlockIP.Allow(accounts.ClientIP(r)) || !s.unlockUser.Allow(userID) || (extra != nil && !extra.Allow(userID)) {
		w.Header().Set("Retry-After", "60")
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "Too many attempts. Wait a minute and try again."})
		return false
	}
	return true
}

// --- enrollment ---

type unlockData struct {
	Next     string
	Recovery bool   // show the recovery-key form
	Error    string // fixed text
}

type enrollData struct{ Enabled bool }

func (s *server) enrollPage(w http.ResponseWriter, r *http.Request, u SessionUser) {
	if !s.st.EnrollEnabled() {
		http.NotFound(w, r)
		return
	}
	st, err := s.encState(r.Context(), u.UserID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if st != "none" {
		http.Redirect(w, r, "/unlock", http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "enroll", enrollData{Enabled: true})
}

func (s *server) enrollBegin(w http.ResponseWriter, r *http.Request, u SessionUser) {
	c, err := s.st.BeginEnroll(r.Context(), u.UserID)
	if err != nil {
		s.pkFail(w, "enroll_begin", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ceremony": c.ID, "options": c.Options})
}

func (s *server) enrollFinish(w http.ResponseWriter, r *http.Request, u SessionUser) {
	var in struct {
		Ceremony    string          `json:"ceremony"`
		Attestation json.RawMessage `json:"attestation"`
		PRFOutput   string          `json:"prf_output"`
		Name        string          `json:"name"`
		ConfirmLoss bool            `json:"confirm_loss"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !in.ConfirmLoss {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Tick the box to confirm you understand that lost keys cannot be recovered."})
		return
	}
	res, err := s.st.FinishEnroll(r.Context(), u.UserID, in.Ceremony, in.Attestation, decodePRF(in.PRFOutput), in.Name)
	if err != nil {
		s.pkFail(w, "enroll_finish", err)
		return
	}
	res.UK.Zero() // the key is not needed here; the confirm round unwraps it again
	writeJSON(w, http.StatusOK, map[string]any{"recovery": res.Recovery, "confirm": res.ConfirmID, "positions": res.Positions})
}

func (s *server) confirmBegin(w http.ResponseWriter, r *http.Request, u SessionUser) {
	var in struct {
		Confirm string `json:"confirm"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	c, err := s.st.BeginConfirm(r.Context(), u.UserID, in.Confirm)
	if err != nil {
		s.pkFail(w, "confirm_begin", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"options": c.Options, "positions": c.Positions})
}

func (s *server) confirmFinish(w http.ResponseWriter, r *http.Request, u SessionUser) {
	var in struct {
		Confirm   string          `json:"confirm"`
		Assertion json.RawMessage `json:"assertion"`
		PRFOutput string          `json:"prf_output"`
		Groups    []string        `json:"groups"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	state, uk, err := s.st.ConfirmEnroll(r.Context(), u.UserID, in.Confirm, in.Assertion, decodePRF(in.PRFOutput), in.Groups)
	if err != nil {
		s.pkFail(w, "confirm", err)
		return
	}
	uk.Zero() // Task 5 turns this unlock into a session wrap
	writeJSON(w, http.StatusOK, map[string]string{"state": state, "next": "/unlock"})
}

// --- unlock ---

func (s *server) unlockPage(w http.ResponseWriter, r *http.Request, u SessionUser) {
	st, err := s.encState(r.Context(), u.UserID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if st == "none" {
		if s.st.EnrollEnabled() {
			http.Redirect(w, r, "/enroll", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "unlock", unlockData{
		Next: safeNext(r.URL.Query().Get("next")), Recovery: r.URL.Query().Get("method") == "recovery"})
}

func (s *server) unlockBegin(w http.ResponseWriter, r *http.Request, u SessionUser) {
	if !s.allowUnlock(w, r, u.UserID, nil) {
		return
	}
	c, err := s.st.BeginUnlock(r.Context(), u.UserID)
	if err != nil {
		s.pkFail(w, "unlock_begin", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ceremony": c.ID, "options": c.Options})
}

func (s *server) unlockFinish(w http.ResponseWriter, r *http.Request, u SessionUser) {
	if !s.allowUnlock(w, r, u.UserID, nil) {
		return
	}
	var in struct {
		Ceremony  string          `json:"ceremony"`
		Assertion json.RawMessage `json:"assertion"`
		PRFOutput string          `json:"prf_output"`
		Next      string          `json:"next"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	uk, err := s.st.FinishUnlock(r.Context(), u.UserID, in.Ceremony, in.Assertion, decodePRF(in.PRFOutput))
	if err != nil {
		s.pkFail(w, "unlock_finish", err)
		return
	}
	uk.Zero() // Task 5 turns this unlock into a session wrap
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "next": safeNext(in.Next)})
}

func (s *server) unlockRecovery(w http.ResponseWriter, r *http.Request, u SessionUser) {
	if !s.allowUnlock(w, r, u.UserID, s.recoveryUser) {
		return
	}
	next := safeNext(r.PostFormValue("next"))
	uk, err := s.st.UnlockWithRecovery(r.Context(), u.UserID, r.PostFormValue("recovery"))
	if err != nil {
		status, msg := http.StatusUnauthorized, "That recovery key did not match."
		if !errors.Is(err, accounts.ErrBadRecovery) {
			status, msg = http.StatusConflict, err.Error()+"."
		}
		s.render(w, r, status, "unlock", unlockData{Next: next, Recovery: true, Error: msg})
		return
	}
	uk.Zero() // Task 5 turns this unlock into a session wrap
	http.Redirect(w, r, next, http.StatusSeeOther)
}
