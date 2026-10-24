package oauth

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/web"
)

var challengeRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func redirectWith(w http.ResponseWriter, r *http.Request, base string, params map[string]string) {
	u, err := url.Parse(base)
	if err != nil {
		http.Error(w, "bad redirect", http.StatusBadRequest)
		return
	}
	q := u.Query()
	for k, v := range params {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

func normalizeScope(s string) (string, bool) {
	fields := strings.Fields(strings.ReplaceAll(s, "+", " "))
	if len(fields) == 0 {
		return "cortex", true
	}
	seen := map[string]bool{}
	var out []string
	for _, f := range fields {
		if f != "cortex" && f != "offline_access" {
			return "", false
		}
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return strings.Join(out, " "), true
}

func (h *handler) authorizeGet(w http.ResponseWriter, r *http.Request, u web.SessionUser) {
	q := r.URL.Query()
	ctx := r.Context()
	cl, err := h.loadClient(ctx, q.Get("client_id"))
	if err != nil {
		http.Error(w, "unknown client", http.StatusBadRequest)
		return
	}
	ru := q.Get("redirect_uri")
	if ru == "" && len(cl.RedirectURIs) == 1 {
		ru = cl.RedirectURIs[0]
	}
	if !cl.hasRedirect(ru) || !validRedirect(ru) {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}
	state := q.Get("state")
	fail := func(code string) {
		redirectWith(w, r, ru, map[string]string{"error": code, "state": state})
	}
	if len(state) > 512 {
		fail("invalid_request")
		return
	}
	if q.Get("response_type") != "code" {
		fail("unsupported_response_type")
		return
	}
	if q.Get("code_challenge_method") != "S256" || !challengeRe.MatchString(q.Get("code_challenge")) {
		fail("invalid_request")
		return
	}
	scope, ok := normalizeScope(q.Get("scope"))
	if !ok {
		fail("invalid_scope")
		return
	}
	id := randToken("ar_")
	now := h.now().UTC()
	if _, err := h.st.DB.ExecContext(ctx,
		`INSERT INTO authorize_requests (id, user_id, client_id, redirect_uri, code_challenge, state, scope, created_at, expires_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		id, u.UserID, cl.ID, ru, q.Get("code_challenge"), state, scope, now, now.Add(requestTTL)); err != nil {
		h.d.Cfg.Logger.Error("oauth authorize", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.d.Render(w, r, "consent", map[string]any{
		"RequestID":    id,
		"ClientName":   cl.Name,
		"RedirectHost": redirectHost(ru),
		"CIMDHost":     cl.CIMDHost,
		"Unverified":   cl.Kind == "dcr",
	})
}

func (h *handler) authorizePost(w http.ResponseWriter, r *http.Request, u web.SessionUser) {
	ctx := r.Context()
	var ar struct{ clientID, redirectURI, challenge, state, scope string }
	// Single use, owned by the session user, unexpired.
	err := h.st.DB.QueryRowContext(ctx,
		`DELETE FROM authorize_requests WHERE id = $1 AND user_id = $2 AND expires_at > $3
		 RETURNING client_id, redirect_uri, code_challenge, state, scope`,
		r.PostFormValue("request_id"), u.UserID, h.now().UTC()).
		Scan(&ar.clientID, &ar.redirectURI, &ar.challenge, &ar.state, &ar.scope)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	cl, err := h.loadClient(ctx, ar.clientID)
	if err != nil || !cl.hasRedirect(ar.redirectURI) || !validRedirect(ar.redirectURI) {
		http.Error(w, "unknown client", http.StatusBadRequest)
		return
	}
	if r.PostFormValue("decision") != "allow" {
		redirectWith(w, r, ar.redirectURI, map[string]string{"error": "access_denied", "state": ar.state})
		return
	}
	connID, err := h.liveConnection(r, u.UserID, cl)
	if err != nil {
		h.d.Cfg.Logger.Error("oauth connection", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	now := h.now().UTC()
	if _, err := h.st.DB.ExecContext(ctx,
		`UPDATE oauth_clients SET authorized_at = COALESCE(authorized_at, $2), expires_at = CASE WHEN kind = 'dcr' THEN NULL ELSE expires_at END WHERE id = $1`,
		cl.ID, now); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	code := randToken("oc_")
	fields, _ := json.Marshal(map[string]string{
		"client_id": cl.ID, "redirect_uri": ar.redirectURI, "code_challenge": ar.challenge, "scope": ar.scope})
	codeHash := accounts.HashToken(code)
	tx, err := h.st.DB.BeginTx(ctx, nil)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO oauth_codes (code_hash, request_fields, user_id, connection_id, expires_at) VALUES ($1,$2,$3,$4,$5)`,
		codeHash, fields, u.UserID, connID, now.Add(codeTTL)); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if u.Key != nil {
		exp := now.Add(codeTTL)
		wrapped, err := keys.Wrap(keys.WrapKeySecret(accounts.WrapKindOAuthCode, code), u.UserID, accounts.WrapKindOAuthCode, codeHash, u.Key)
		if err == nil {
			err = h.st.PutWrapTx(ctx, tx, accounts.Wrap{UserID: u.UserID, Kind: accounts.WrapKindOAuthCode, RefID: codeHash, Wrapped: wrapped, ExpiresAt: &exp})
		}
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	redirectWith(w, r, ar.redirectURI, map[string]string{"code": code, "state": ar.state})
}

// liveConnection returns the user's live connection for the client,
// creating it when none exists.
func (h *handler) liveConnection(r *http.Request, userID string, cl *client) (string, error) {
	ctx := r.Context()
	find := func() (string, error) {
		var id string
		err := h.st.DB.QueryRowContext(ctx,
			`SELECT id FROM connections WHERE user_id = $1 AND client_id = $2 AND kind = 'oauth' AND revoked_at IS NULL`,
			userID, cl.ID).Scan(&id)
		return id, err
	}
	if id, err := find(); err == nil {
		return id, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	c, err := h.st.CreateConnection(ctx, userID, "oauth", cl.Name, cl.ID)
	if err != nil {
		if id, ferr := find(); ferr == nil { // lost a race to the unique index
			return id, nil
		}
		return "", err
	}
	return c.ID, nil
}
