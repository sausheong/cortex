package web

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/sausheong/cortex/internal/accounts"
)

// connectionInstructions is the custom-instruction text shown on the page.
const connectionInstructions = "You have access to my cortex memory. Before answering anything that depends on my preferences, how I'd do things, or people and things I know, call the cortex `context` tool. Whenever you learn something new about me — a preference, a person or organisation I know, a capability, my background — save it with cortex `remember`."

type connView struct {
	ID, Name, LastUsed string
	Written            int
	Revoked            bool
}

type connectionsView struct {
	Assistants, Agents []connView
	NewToken, NewName  string
	Error              string
	Instructions       string
}

// ConnectionsMount registers the connections page routes.
func ConnectionsMount(mux *http.ServeMux, d Deps) {
	h := &connHandler{d: d}
	mux.HandleFunc("GET /connections", d.Auth(h.list))
	mux.HandleFunc("POST /connections/tokens", d.Auth(h.newToken))
	mux.HandleFunc("POST /connections/{id}/revoke", d.Auth(h.revoke))
}

type connHandler struct{ d Deps }

func (h *connHandler) view(r *http.Request, u SessionUser) (connectionsView, error) {
	conns, err := h.d.Accounts.ListConnections(r.Context(), u.UserID)
	v := connectionsView{Instructions: connectionInstructions}
	if err != nil {
		return v, err
	}
	for _, c := range conns {
		cv := connView{ID: c.ID, Name: c.Name, Written: c.Written, Revoked: c.RevokedAt != nil}
		if c.LastUsed != nil {
			cv.LastUsed = c.LastUsed.UTC().Format(time.RFC3339)
		}
		if c.Kind == "agent" {
			v.Agents = append(v.Agents, cv)
		} else {
			v.Assistants = append(v.Assistants, cv)
		}
	}
	return v, nil
}

func (h *connHandler) page(w http.ResponseWriter, r *http.Request, u SessionUser, mod func(*connectionsView)) {
	v, err := h.view(r, u)
	if err != nil {
		h.d.Cfg.Logger.Error("connections", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if mod != nil {
		mod(&v)
	}
	h.d.Render(w, r, "connections", v)
}

func (h *connHandler) list(w http.ResponseWriter, r *http.Request, u SessionUser) {
	h.page(w, r, u, nil)
}

func (h *connHandler) newToken(w http.ResponseWriter, r *http.Request, u SessionUser) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" || len(name) > 80 {
		h.page(w, r, u, func(v *connectionsView) { v.Error = "Enter a token name (up to 80 characters)." })
		return
	}
	tok, _, err := h.d.Accounts.CreateAgentToken(r.Context(), u.UserID, name)
	if err != nil {
		h.d.Cfg.Logger.Error("create token", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.page(w, r, u, func(v *connectionsView) { v.NewToken, v.NewName = tok, name })
}

func (h *connHandler) revoke(w http.ResponseWriter, r *http.Request, u SessionUser) {
	id := r.PathValue("id")
	if err := h.d.Accounts.RevokeConnection(r.Context(), u.UserID, id); err != nil {
		if errors.Is(err, accounts.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		h.d.Cfg.Logger.Error("revoke", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if r.PostFormValue("remove") == "1" {
		if _, _, err := h.d.Root.ForUser(u.UserID).SoftDeleteByConnection(r.Context(), id); err != nil {
			h.d.Cfg.Logger.Error("remove by connection", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}
	http.Redirect(w, r, "/connections", http.StatusSeeOther)
}
