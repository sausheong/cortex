package oauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/sausheong/cortex/internal/accounts"
)

func (h *handler) token(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "malformed request")
		return
	}
	if _, err := h.loadClient(r.Context(), r.PostFormValue("client_id")); err != nil {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "unknown client")
		return
	}
	switch r.PostFormValue("grant_type") {
	case "authorization_code":
		h.exchangeCode(w, r)
	case "refresh_token":
		h.refresh(w, r)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "unsupported grant_type")
	}
}

func invalidGrant(w http.ResponseWriter) {
	oauthError(w, http.StatusBadRequest, "invalid_grant", "invalid or expired grant")
}

func pkceOK(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(challenge)) == 1
}

type issued struct{ access, refresh string }

// issue stores a fresh access/refresh pair of grantID.
func (h *handler) issue(tx *sql.Tx, r *http.Request, userID, connID, grantID string, now time.Time) (issued, error) {
	out := issued{access: randToken("oat_"), refresh: randToken("ort_")}
	for _, t := range []struct {
		tok, kind string
		ttl       time.Duration
	}{{out.access, "access", accessTTL}, {out.refresh, "refresh", refreshTTL}} {
		if _, err := tx.ExecContext(r.Context(),
			`INSERT INTO oauth_tokens (token_hash, kind, grant_id, connection_id, user_id, expires_at) VALUES ($1,$2,$3,$4,$5,$6)`,
			accounts.HashToken(t.tok), t.kind, grantID, connID, userID, now.Add(t.ttl)); err != nil {
			return issued{}, err
		}
	}
	return out, nil
}

func (h *handler) respond(w http.ResponseWriter, t issued, scope string) {
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": t.access, "token_type": "Bearer", "expires_in": int(accessTTL.Seconds()),
		"refresh_token": t.refresh, "scope": scope,
	})
}

func (h *handler) exchangeCode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := h.now().UTC()
	codeHash := accounts.HashToken(r.PostFormValue("code"))
	tx, err := h.st.DB.BeginTx(ctx, nil)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "internal error")
		return
	}
	defer tx.Rollback()
	var fields []byte
	var userID, connID string
	err = tx.QueryRowContext(ctx,
		`UPDATE oauth_codes c SET used_at = $2 WHERE c.code_hash = $1 AND c.used_at IS NULL AND c.expires_at > $2
		 AND EXISTS (SELECT 1 FROM users u WHERE u.id = c.user_id AND u.deleted_at IS NULL)
		 AND EXISTS (SELECT 1 FROM connections n WHERE n.id = c.connection_id AND n.revoked_at IS NULL)
		 RETURNING request_fields, user_id, connection_id`, codeHash, now).Scan(&fields, &userID, &connID)
	if errors.Is(err, sql.ErrNoRows) {
		tx.Rollback()
		// A replayed code revokes whatever it already produced.
		_, _ = h.st.DB.ExecContext(ctx, `UPDATE oauth_tokens SET revoked_at = $2 WHERE grant_id = $1 AND revoked_at IS NULL`, codeHash, now)
		invalidGrant(w)
		return
	}
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "internal error")
		return
	}
	var f map[string]string
	if json.Unmarshal(fields, &f) != nil ||
		f["client_id"] != r.PostFormValue("client_id") ||
		f["redirect_uri"] != r.PostFormValue("redirect_uri") ||
		!pkceOK(r.PostFormValue("code_verifier"), f["code_challenge"]) {
		_ = tx.Commit() // the code stays burned
		invalidGrant(w)
		return
	}
	t, err := h.issue(tx, r, userID, connID, codeHash, now)
	if err != nil || tx.Commit() != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "internal error")
		return
	}
	h.respond(w, t, f["scope"])
}

func (h *handler) refresh(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := h.now().UTC()
	hash := accounts.HashToken(r.PostFormValue("refresh_token"))
	tx, err := h.st.DB.BeginTx(ctx, nil)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "internal error")
		return
	}
	defer tx.Rollback()
	var userID, connID, grant, clientID string
	var exp time.Time
	var revoked sql.NullTime
	var connRevoked sql.NullTime
	var deleted sql.NullTime
	err = tx.QueryRowContext(ctx,
		`SELECT t.user_id, t.connection_id, t.grant_id, t.expires_at, t.revoked_at, c.client_id, c.revoked_at, u.deleted_at
		 FROM oauth_tokens t JOIN connections c ON c.id = t.connection_id JOIN users u ON u.id = t.user_id
		 WHERE t.token_hash = $1 AND t.kind = 'refresh' FOR UPDATE OF t`, hash).
		Scan(&userID, &connID, &grant, &exp, &revoked, &clientID, &connRevoked, &deleted)
	if err != nil {
		invalidGrant(w)
		return
	}
	if revoked.Valid {
		// Reuse of a rotated token: the whole grant is compromised.
		_, _ = tx.ExecContext(ctx, `UPDATE oauth_tokens SET revoked_at = $2 WHERE grant_id = $1 AND revoked_at IS NULL`, grant, now)
		_ = tx.Commit()
		invalidGrant(w)
		return
	}
	if !exp.After(now) || connRevoked.Valid || deleted.Valid || clientID != r.PostFormValue("client_id") {
		invalidGrant(w)
		return
	}
	if _, err := tx.ExecContext(ctx, `UPDATE oauth_tokens SET revoked_at = $2, last_used_at = $2 WHERE token_hash = $1`, hash, now); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "internal error")
		return
	}
	// Superseded access tokens of this grant die with the rotation.
	_, _ = tx.ExecContext(ctx, `UPDATE oauth_tokens SET revoked_at = $2 WHERE grant_id = $1 AND kind = 'access' AND revoked_at IS NULL`, grant, now)
	t, err := h.issue(tx, r, userID, connID, grant, now)
	if err != nil || tx.Commit() != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "internal error")
		return
	}
	h.respond(w, t, "cortex offline_access")
}
