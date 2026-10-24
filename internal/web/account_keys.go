package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"
)

// Account key management: passkeys, the recovery key and "Forget all
// devices". Every POST goes through s.auth, so it carries the session's CSRF
// token. The ones that change key material use authUnlocked: they need the
// user's key in the request. Nothing here logs a secret or echoes one except
// the new recovery key, shown once in the response to its own request.

const (
	lostKeysMsg    = "Tick the box to confirm you understand that lost keys cannot be recovered."
	lastPasskeyMsg = "To remove your last passkey, enter your recovery key exactly as you saved it. Nothing was removed."
)

func (s *server) mountAccountKeys(mux *http.ServeMux) {
	mux.HandleFunc("GET /account", s.auth(s.accountGet))
	mux.HandleFunc("POST /account/passkeys/add/begin", s.authUnlocked(s.accountAddBegin))
	mux.HandleFunc("POST /account/passkeys/add/finish", s.authUnlocked(s.accountAddFinish))
	mux.HandleFunc("POST /account/passkeys/{id}/remove", s.authUnlocked(s.accountRemovePasskey))
	mux.HandleFunc("POST /account/recovery/regenerate", s.authUnlocked(s.accountRegenerate))
	mux.HandleFunc("POST /account/forget-devices", s.auth(s.accountForget))
}

type passkeyView struct{ ID, Name, Created, LastUsed string }

type keysView struct {
	// Enrolled is true once the account is protected by a passkey.
	Enrolled bool
	// Enabled is true when enrollment is switched on (the link to set up).
	Enabled bool
	// Unlocked is true when this request carries the user's key; the forms
	// that change key material need it.
	Unlocked bool
	Passkeys []passkeyView
	// NewRecovery is the freshly generated recovery key, shown once.
	NewRecovery string
	Error       string
	Notice      string
}

type accountView struct {
	Usage cortex.Usage
	MB    string
	Keys  keysView
}

func (s *server) accountGet(w http.ResponseWriter, r *http.Request, u SessionUser) {
	s.accountPage(w, r, u, http.StatusOK, nil)
}

func (s *server) accountPage(w http.ResponseWriter, r *http.Request, u SessionUser, status int, mod func(*keysView)) {
	ctx := r.Context()
	cx, release := cxFor(ctx, s.root, s.st, u)
	defer release()
	us, err := cx.Usage(ctx)
	if err != nil {
		s.cfg.Logger.Error("usage", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	mb := "Under 0.01 MB"
	if us.Bytes >= (1<<20)/100 {
		mb = strconv.FormatFloat(float64(us.Bytes)/(1<<20), 'f', 2, 64) + " MB"
	}
	kv := keysView{Enabled: s.st.EnrollEnabled(), Unlocked: u.Unlocked}
	info, err := s.st.EnrollInfo(ctx, u.UserID)
	if err != nil {
		s.cfg.Logger.Error("account keys", "step", "state")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	kv.Enrolled = info.State != "none"
	if kv.Enrolled {
		pks, err := s.st.ListPasskeys(ctx, u.UserID)
		if err != nil {
			s.cfg.Logger.Error("account keys", "step", "list")
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		for _, p := range pks {
			v := passkeyView{ID: p.ID, Name: p.Name, Created: p.CreatedAt.UTC().Format("2 Jan 2006")}
			if p.LastUsed != nil {
				v.LastUsed = p.LastUsed.UTC().Format("2 Jan 2006")
			}
			kv.Passkeys = append(kv.Passkeys, v)
		}
	}
	q := r.URL.Query()
	switch {
	case q.Get("added") == "1":
		kv.Notice = "Passkey added. It can unlock your memory from now on."
	case q.Get("removed") == "1":
		kv.Notice = "Passkey removed. It can no longer unlock your memory."
	case q.Get("forgot") == "1":
		kv.Notice = "Every other browser is locked again, and OAuth assistants were disconnected. Reconnect them from Connections."
	}
	if mod != nil {
		mod(&kv)
	}
	s.render(w, r, status, "account", accountView{Usage: us, MB: mb, Keys: kv})
}

func (s *server) accountAddBegin(w http.ResponseWriter, r *http.Request, u SessionUser) {
	c, err := s.st.BeginAddPasskey(r.Context(), u.UserID)
	if err != nil {
		s.pkFail(w, "add_begin", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ceremony": c.ID, "options": c.Options})
}

func (s *server) accountAddFinish(w http.ResponseWriter, r *http.Request, u SessionUser) {
	var in struct {
		Ceremony    string          `json:"ceremony"`
		Attestation json.RawMessage `json:"attestation"`
		PRFOutput   string          `json:"prf_output"`
		Name        string          `json:"name"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if err := s.st.AddPasskey(r.Context(), u.UserID, u.Key, in.Ceremony, in.Attestation, decodePRF(in.PRFOutput), in.Name); err != nil {
		s.pkFail(w, "add_finish", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *server) accountRemovePasskey(w http.ResponseWriter, r *http.Request, u SessionUser) {
	rec := r.PostFormValue("recovery")
	if rec != "" && !s.recoveryUser.Allow(u.UserID) {
		w.Header().Set("Retry-After", "60")
		s.accountPage(w, r, u, http.StatusTooManyRequests, func(k *keysView) {
			k.Error = "Too many attempts. Wait a minute and try again."
		})
		return
	}
	err := s.st.RemovePasskey(r.Context(), u.UserID, r.PathValue("id"), u.Key, rec)
	switch {
	case err == nil:
		http.Redirect(w, r, "/account?removed=1", http.StatusSeeOther)
	case errors.Is(err, accounts.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, accounts.ErrBadRecovery):
		s.accountPage(w, r, u, http.StatusUnauthorized, func(k *keysView) { k.Error = lastPasskeyMsg })
	case errors.Is(err, accounts.ErrNotEnrolled):
		s.accountPage(w, r, u, http.StatusConflict, func(k *keysView) { k.Error = err.Error() + "." })
	default:
		s.cfg.Logger.Error("passkey", "step", "remove")
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *server) accountRegenerate(w http.ResponseWriter, r *http.Request, u SessionUser) {
	if r.PostFormValue("confirm_loss") == "" {
		s.accountPage(w, r, u, http.StatusBadRequest, func(k *keysView) { k.Error = lostKeysMsg })
		return
	}
	rec, err := s.st.RegenerateRecovery(r.Context(), u.UserID, u.Key)
	switch {
	case err == nil:
		s.accountPage(w, r, u, http.StatusOK, func(k *keysView) { k.NewRecovery = rec })
	case errors.Is(err, accounts.ErrNotEnrolled):
		s.accountPage(w, r, u, http.StatusConflict, func(k *keysView) { k.Error = err.Error() + "." })
	default:
		s.cfg.Logger.Error("recovery", "step", "regenerate")
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (s *server) accountForget(w http.ResponseWriter, r *http.Request, u SessionUser) {
	keep := ""
	if ck, err := r.Cookie(sessionCookie); err == nil {
		keep = accounts.HashToken(ck.Value)
	}
	err := s.st.ForgetDevices(r.Context(), u.UserID, keep)
	switch {
	case err == nil:
		http.Redirect(w, r, "/account?forgot=1", http.StatusSeeOther)
	case errors.Is(err, accounts.ErrNotEnrolled):
		s.accountPage(w, r, u, http.StatusConflict, func(k *keysView) { k.Error = err.Error() + "." })
	default:
		s.cfg.Logger.Error("forget devices", "step", "delete")
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
