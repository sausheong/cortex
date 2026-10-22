package oauth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

func (h *handler) register(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, registerCap)
	var in struct {
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			oauthError(w, http.StatusRequestEntityTooLarge, "invalid_client_metadata", "request too large")
			return
		}
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "malformed JSON")
		return
	}
	if len(in.RedirectURIs) == 0 || len(in.RedirectURIs) > 10 {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris required")
		return
	}
	for _, u := range in.RedirectURIs {
		if !validRedirect(u) {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect URIs must be https, or http to localhost")
			return
		}
	}
	name := strings.TrimSpace(in.ClientName)
	if name == "" {
		name = "Unnamed assistant"
	}
	if len(name) > 100 {
		name = name[:100]
	}
	id := randToken("dcr_")
	now := h.now().UTC()
	uris, _ := json.Marshal(in.RedirectURIs)
	if _, err := h.st.DB.ExecContext(r.Context(),
		`INSERT INTO oauth_clients (id, kind, name, redirect_uris, created_at, expires_at) VALUES ($1,'dcr',$2,$3,$4,$5)`,
		id, name, uris, now, now.Add(dcrTTL)); err != nil {
		h.d.Cfg.Logger.Error("oauth register", "err", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id": id, "client_name": name, "redirect_uris": in.RedirectURIs,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"client_id_issued_at":        now.Unix(),
	})
}
