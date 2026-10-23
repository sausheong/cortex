package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sausheong/cortex/internal/accounts"
)

// connectionInstructions is the custom-instruction text shown on the page.
const connectionInstructions = "You have access to my cortex memory. Before answering anything that depends on my preferences, how I'd do things, or people and things I know, call the cortex `context` tool. Whenever you learn something new about me (a preference, a person or organisation I know, a capability, my background), save it with cortex `remember`."

// ago renders an elapsed duration the way people say it.
func ago(d time.Duration) string {
	plural := func(n int, unit string) string {
		if n == 1 {
			return "1 " + unit + " ago"
		}
		return strconv.Itoa(n) + " " + unit + "s ago"
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute")
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour")
	case d < 30*24*time.Hour:
		return plural(int(d.Hours()/24), "day")
	case d < 365*24*time.Hour:
		return plural(int(d.Hours()/24/30), "month")
	}
	return plural(int(d.Hours()/24/365), "year")
}

type connView struct {
	ID, Name, LastUsed string
	// LastUsedAgo is the friendly form of LastUsed ("3 days ago"); empty when never used.
	LastUsedAgo string
	Written     int
	Revoked     bool
	// DeleteCount is how many items "Revoke and delete" would remove.
	DeleteCount int
	CSRF        string
}

// connNotice is a one-line status after a rename or remove.
type connNotice struct {
	Text string
	// UndoID is set after a remove; Undo un-hides that connection.
	UndoID string
}

// revokeNotice is the persistent message after a revoke.
type revokeNotice struct {
	Name    string
	BatchID string // set when items were deleted and Undo is available
	Items   int
}

type connectionsView struct {
	Assistants, Agents []connView
	NewToken, NewName  string
	Error              string
	ErrField           string // "name" ties Error to the token name field
	FormName           string // token name to redisplay after an error
	Instructions       string
	Revoked            *revokeNotice
	WasRestored        bool
	Notice             *connNotice
}

// ConnectionsMount registers the connections page routes.
func ConnectionsMount(mux *http.ServeMux, d Deps) {
	h := &connHandler{d: d}
	mux.HandleFunc("GET /connections", d.Auth(h.list))
	mux.HandleFunc("POST /connections/tokens", d.Auth(h.newToken))
	mux.HandleFunc("POST /connections/{id}/revoke", d.Auth(h.revoke))
	mux.HandleFunc("POST /connections/{id}/rename", d.Auth(h.rename))
	mux.HandleFunc("POST /connections/{id}/remove", d.Auth(h.remove))
	mux.HandleFunc("POST /connections/{id}/unremove", d.Auth(h.unremove))
}

type connHandler struct{ d Deps }

func (h *connHandler) view(r *http.Request, u SessionUser) (connectionsView, error) {
	conns, err := h.d.Accounts.ListConnections(r.Context(), u.UserID)
	v := connectionsView{Instructions: connectionInstructions}
	if err != nil {
		return v, err
	}
	cx := h.d.Root.ForUser(u.UserID)
	q := r.URL.Query()
	if _, err := strconv.Atoi(q.Get("restored")); err == nil {
		v.WasRestored = true
	}
	for _, c := range conns {
		if c.RemovedAt != nil {
			if q.Get("removed") == c.ID {
				v.Notice = &connNotice{Text: "Removed " + c.Name + ". Nothing it wrote was deleted.", UndoID: c.ID}
			}
			continue
		}
		if q.Get("renamed") == c.ID {
			v.Notice = &connNotice{Text: "Renamed to " + c.Name + "."}
		}
		cv := connView{ID: c.ID, Name: c.Name, Written: c.Written, Revoked: c.RevokedAt != nil, CSRF: u.CSRF}
		if c.RevokedAt == nil {
			if cv.DeleteCount, err = cx.CountByConnection(r.Context(), c.ID); err != nil {
				return v, err
			}
		}
		if q.Get("revoked") == c.ID && c.RevokedAt != nil {
			v.Revoked = &revokeNotice{Name: c.Name}
			// Build the undo from the stored batch, not the URL's numbers.
			if b := q.Get("batch"); b != "" {
				if bs, err := cx.ListDeleteBatches(r.Context()); err == nil {
					for _, d := range bs {
						if d.ID == b && d.Kind == "connection" {
							v.Revoked.BatchID = d.ID
							v.Revoked.Items = d.Memories + d.Entities + d.Relationships + d.Chunks
						}
					}
				}
			}
		}
		if c.LastUsed != nil {
			cv.LastUsed = c.LastUsed.UTC().Format(time.RFC3339)
			cv.LastUsedAgo = ago(time.Since(*c.LastUsed))
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
	h.pageStatus(w, r, u, http.StatusOK, mod)
}

func (h *connHandler) pageStatus(w http.ResponseWriter, r *http.Request, u SessionUser, status int, mod func(*connectionsView)) {
	v, err := h.view(r, u)
	if err != nil {
		h.d.Cfg.Logger.Error("connections", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if mod != nil {
		mod(&v)
	}
	h.d.RenderStatus(w, r, status, "connections", v)
}

func (h *connHandler) list(w http.ResponseWriter, r *http.Request, u SessionUser) {
	h.page(w, r, u, nil)
}

func (h *connHandler) newToken(w http.ResponseWriter, r *http.Request, u SessionUser) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" || len(name) > 80 {
		h.page(w, r, u, func(v *connectionsView) {
			v.Error, v.ErrField, v.FormName = "Enter a name for the token, up to 80 characters. For example: laptop agent.", "name", name
		})
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
	ctx := r.Context()
	id := r.PathValue("id")
	cx := h.d.Root.ForUser(u.UserID)
	conns, err := h.d.Accounts.ListConnections(ctx, u.UserID)
	if err != nil {
		h.d.Cfg.Logger.Error("revoke", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var conn *accounts.Connection
	for i := range conns {
		if conns[i].ID == id {
			conn = &conns[i]
		}
	}
	if conn == nil {
		http.NotFound(w, r)
		return
	}
	choice := r.PostFormValue("choice")
	if choice != "keep" && choice != "delete" {
		h.page(w, r, u, func(v *connectionsView) {
			v.Error = "Choose what happens to what " + conn.Name + " wrote: keep it, or delete it. Nothing was revoked."
		})
		return
	}
	n := 0
	if choice == "delete" {
		if n, err = cx.CountByConnection(ctx, id); err != nil {
			h.d.Cfg.Logger.Error("revoke count", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		want, has := expectCount(r)
		if !has || want != n {
			cv := confirmView{
				Title:       "Revoke " + conn.Name + " and delete " + plural(n, "item", "items") + "?",
				Consequence: conn.Name + " loses access right away, and the " + plural(n, "item", "items") + " it wrote are deleted. You can restore the items for 30 days. The connection stays revoked.",
				Action:      "/connections/" + id + "/revoke",
				Fields:      []confirmField{{"choice", "delete"}},
				Count:       n,
				Button:      "Revoke and delete " + plural(n, "item", "items"),
				CancelHref:  "/connections",
			}
			status := http.StatusOK
			if has {
				cv.Error, status = changedMsg(want, n, "items it wrote"), http.StatusConflict
			}
			h.d.RenderStatus(w, r, status, "confirm", cv)
			return
		}
	}
	if err := h.d.Accounts.RevokeConnection(ctx, u.UserID, id); err != nil {
		if errors.Is(err, accounts.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		h.d.Cfg.Logger.Error("revoke", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	dest := "/connections?revoked=" + url.QueryEscape(id)
	if choice == "delete" {
		batch, _, err := cx.SoftDeleteByConnection(ctx, id)
		if err != nil {
			h.d.Cfg.Logger.Error("remove by connection", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if batch != "" {
			dest += "&batch=" + url.QueryEscape(batch)
		}
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func (h *connHandler) rename(w http.ResponseWriter, r *http.Request, u SessionUser) {
	id := r.PathValue("id")
	name := strings.TrimSpace(r.PostFormValue("name"))
	err := h.d.Accounts.RenameConnection(r.Context(), u.UserID, id, name)
	switch {
	case errors.Is(err, accounts.ErrBadName):
		h.pageStatus(w, r, u, http.StatusBadRequest, func(v *connectionsView) {
			v.Error = "Enter a name from 1 to 80 characters. The name was not changed."
		})
	case errors.Is(err, accounts.ErrNotFound):
		http.NotFound(w, r)
	case err != nil:
		h.d.Cfg.Logger.Error("rename connection", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	default:
		http.Redirect(w, r, "/connections?renamed="+url.QueryEscape(id), http.StatusSeeOther)
	}
}

func (h *connHandler) remove(w http.ResponseWriter, r *http.Request, u SessionUser) {
	id := r.PathValue("id")
	err := h.d.Accounts.RemoveConnection(r.Context(), u.UserID, id)
	switch {
	case errors.Is(err, accounts.ErrConnectionActive):
		h.pageStatus(w, r, u, http.StatusConflict, func(v *connectionsView) {
			v.Error = "Revoke this connection before removing it. Nothing was removed."
		})
	case errors.Is(err, accounts.ErrNotFound):
		http.NotFound(w, r)
	case err != nil:
		h.d.Cfg.Logger.Error("remove connection", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	default:
		http.Redirect(w, r, "/connections?removed="+url.QueryEscape(id), http.StatusSeeOther)
	}
}

func (h *connHandler) unremove(w http.ResponseWriter, r *http.Request, u SessionUser) {
	err := h.d.Accounts.UnremoveConnection(r.Context(), u.UserID, r.PathValue("id"))
	switch {
	case errors.Is(err, accounts.ErrNotFound):
		http.NotFound(w, r)
	case err != nil:
		h.d.Cfg.Logger.Error("unremove connection", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	default:
		http.Redirect(w, r, "/connections", http.StatusSeeOther)
	}
}
