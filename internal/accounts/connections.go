package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/model"
	"strings"
	"time"
	"unicode/utf8"
)

// Connection is a way a client reaches a user's memory: an OAuth-authorized
// assistant or a named agent token.
type Connection struct {
	ID, UserID, Kind /* "oauth"|"agent" */, Name, ClientID string
	CreatedAt                                              time.Time
	LastUsed                                               *time.Time
	Written                                                int
	RevokedAt                                              *time.Time
	// RemovedAt is set when the user hid the connection from their list.
	RemovedAt *time.Time
	// ExpiresAt is the agent token's expiry (nil: none). NeedsReconnect marks
	// a connection whose token carries no key wrap after encryption.
	ExpiresAt      *time.Time
	NeedsReconnect bool
}

// HashToken is the SHA-256 hex digest under which tokens are stored.
func HashToken(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

// CreateConnection inserts a live connection. A second live oauth
// connection for one (user, client_id) fails (partial unique index).
func (st *Store) CreateConnection(ctx context.Context, userID, kind, name, clientID string) (*Connection, error) {
	if kind != "oauth" && kind != "agent" {
		return nil, fmt.Errorf("accounts: bad connection kind %q", kind)
	}
	return st.createConnection(ctx, st.DB, userID, kind, name, clientID)
}

type execer interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
}

func (st *Store) createConnection(ctx context.Context, ex execer, userID, kind, name, clientID string) (*Connection, error) {
	c := &Connection{ID: "c_" + randHex(12), UserID: userID, Kind: kind, Name: name, ClientID: clientID, CreatedAt: st.now()}
	if _, err := ex.ExecContext(ctx, `INSERT INTO connections (id, user_id, kind, name, client_id, created_at) VALUES ($1,$2,$3,$4,$5,$6)`,
		c.ID, userID, kind, name, clientID, c.CreatedAt); err != nil {
		return nil, fmt.Errorf("accounts: create connection: %w", err)
	}
	return c, nil
}

// ListConnections returns the user's connections (live and revoked), newest
// first, with the count of memories each wrote.
func (st *Store) ListConnections(ctx context.Context, userID string) ([]Connection, error) {
	rows, err := st.DB.QueryContext(ctx, `SELECT c.id, c.user_id, c.kind, c.name, c.client_id, c.created_at, c.last_used, c.revoked_at, c.removed_at,
		(SELECT max(t.expires_at) FROM agent_tokens t WHERE t.connection_id = c.id), c.needs_reconnect,
		(SELECT count(*) FROM memories m WHERE m.user_id = c.user_id AND m.written_by_connection_id = c.id AND m.deleted_at IS NULL)
		FROM connections c WHERE c.user_id = $1 ORDER BY c.created_at DESC, c.id`, userID)
	if err != nil {
		return nil, fmt.Errorf("accounts: list connections: %w", err)
	}
	defer rows.Close()
	var out []Connection
	for rows.Next() {
		var c Connection
		var lu, rv, rm, ex sql.NullTime
		if err := rows.Scan(&c.ID, &c.UserID, &c.Kind, &c.Name, &c.ClientID, &c.CreatedAt, &lu, &rv, &rm, &ex, &c.NeedsReconnect, &c.Written); err != nil {
			return nil, err
		}
		if ex.Valid {
			t := ex.Time
			c.ExpiresAt = &t
		}
		if lu.Valid {
			t := lu.Time
			c.LastUsed = &t
		}
		if rv.Valid {
			t := rv.Time
			c.RevokedAt = &t
		}
		if rm.Valid {
			t := rm.Time
			c.RemovedAt = &t
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RevokeConnection revokes the connection and invalidates its tokens. A
// foreign or absent ID returns the same ErrNotFound.
func (st *Store) RevokeConnection(ctx context.Context, userID, connID string) error {
	tx, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM connections WHERE id = $1 AND user_id = $2 FOR UPDATE`, connID, userID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	now := st.now()
	if _, err := tx.ExecContext(ctx, `UPDATE connections SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`, connID, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE oauth_tokens SET revoked_at = $2 WHERE connection_id = $1 AND revoked_at IS NULL`, connID, now); err != nil {
		return err
	}
	if err := deleteAgentWraps(ctx, tx, userID, connID); err != nil {
		return err
	}
	if err := deleteOAuthWraps(ctx, tx, userID, connID); err != nil {
		return err
	}
	return tx.Commit()
}

// ErrBadName means a connection name is empty or longer than 80 characters.
var ErrBadName = errors.New("accounts: connection name must be 1-80 characters")

// ErrConnectionActive means the connection must be revoked before removal.
var ErrConnectionActive = errors.New("accounts: revoke the connection before removing it")

// RenameConnection sets the display name of the user's own, non-removed
// connection. A foreign, removed or absent ID returns ErrNotFound.
func (st *Store) RenameConnection(ctx context.Context, userID, connID, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 80 {
		return ErrBadName
	}
	res, err := st.DB.ExecContext(ctx, `UPDATE connections SET name = $3 WHERE id = $1 AND user_id = $2 AND removed_at IS NULL`, connID, userID, name)
	if err != nil {
		return fmt.Errorf("accounts: rename connection: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RemoveConnection hides a revoked connection from the user's list. Nothing
// is deleted. An active connection returns ErrConnectionActive; a foreign or
// absent ID returns ErrNotFound.
func (st *Store) RemoveConnection(ctx context.Context, userID, connID string) error {
	var revoked sql.NullTime
	err := st.DB.QueryRowContext(ctx, `SELECT revoked_at FROM connections WHERE id = $1 AND user_id = $2`, connID, userID).Scan(&revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !revoked.Valid {
		return ErrConnectionActive
	}
	_, err = st.DB.ExecContext(ctx, `UPDATE connections SET removed_at = COALESCE(removed_at, $3) WHERE id = $1 AND user_id = $2`, connID, userID, st.now())
	return err
}

// UnremoveConnection undoes RemoveConnection. A foreign or absent ID returns ErrNotFound.
func (st *Store) UnremoveConnection(ctx context.Context, userID, connID string) error {
	res, err := st.DB.ExecContext(ctx, `UPDATE connections SET removed_at = NULL WHERE id = $1 AND user_id = $2`, connID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AgentTokenLifetime is how long an agent token (and its key wrap) stays valid.
const AgentTokenLifetime = 90 * 24 * time.Hour

const wrapKindAgent = "agent"

// CreateAgentToken creates an agent connection and returns its plaintext
// token (shown once; only the hash is stored). For a user who has enrolled
// encryption (state other than "none") uk is required (model.ErrLocked
// otherwise); the token then carries a wrap of uk under a key derived from
// the token and expires after AgentTokenLifetime. A legacy user with a nil uk
// gets a non-expiring token with no wrap.
func (st *Store) CreateAgentToken(ctx context.Context, userID, name string, uk *keys.UK) (string, *Connection, error) {
	state, err := st.encState(ctx, userID)
	if err != nil {
		return "", nil, err
	}
	if state != "none" && uk == nil {
		return "", nil, model.ErrLocked
	}
	plain, err := newAgentTokenString()
	if err != nil {
		return "", nil, err
	}
	tx, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback()
	conn, err := st.createConnection(ctx, tx, userID, "agent", name, "")
	if err != nil {
		return "", nil, err
	}
	if err := st.insertAgentToken(ctx, tx, userID, conn.ID, plain, uk); err != nil {
		return "", nil, err
	}
	if err := tx.Commit(); err != nil {
		return "", nil, err
	}
	return plain, conn, nil
}

func newAgentTokenString() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "ctx_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

// insertAgentToken stores the token hash and, when uk is non-nil, the wrap and
// the 90-day expiry.
func (st *Store) insertAgentToken(ctx context.Context, tx *sql.Tx, userID, connID, plain string, uk *keys.UK) error {
	id := "t_" + randHex(12)
	var exp *time.Time
	if uk != nil {
		e := st.now().Add(AgentTokenLifetime)
		exp = &e
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_tokens (id, connection_id, token_hash, expires_at) VALUES ($1,$2,$3,$4)`,
		id, connID, HashToken(plain), exp); err != nil {
		return fmt.Errorf("accounts: create token: %w", err)
	}
	if uk == nil {
		return nil
	}
	wrapped, err := keys.Wrap(keys.WrapKeySecret(wrapKindAgent, plain), userID, wrapKindAgent, id, uk)
	if err != nil {
		return err
	}
	return st.PutWrapTx(ctx, tx, Wrap{UserID: userID, Kind: wrapKindAgent, RefID: id, Wrapped: wrapped, ExpiresAt: exp})
}

// RenewAgentToken replaces the connection's token with a fresh one (new
// 90-day wrap). The old token, its hash and its wrap are deleted in the same
// transaction, so the old token stops working at once. uk is required for
// an encrypted user (model.ErrLocked otherwise).
func (st *Store) RenewAgentToken(ctx context.Context, userID, connID string, uk *keys.UK) (string, error) {
	if uk == nil {
		if state, err := st.encState(ctx, userID); err != nil {
			return "", err
		} else if state != "none" {
			return "", model.ErrLocked
		}
	}
	plain, err := newAgentTokenString()
	if err != nil {
		return "", err
	}
	tx, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM connections WHERE id = $1 AND user_id = $2 AND kind = 'agent'
		AND revoked_at IS NULL AND removed_at IS NULL FOR UPDATE`, connID, userID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if err := deleteAgentWraps(ctx, tx, userID, connID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_tokens WHERE connection_id = $1`, connID); err != nil {
		return "", err
	}
	if err := st.insertAgentToken(ctx, tx, userID, connID, plain, uk); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE connections SET needs_reconnect = FALSE WHERE id = $1`, connID); err != nil {
		return "", err
	}
	return plain, tx.Commit()
}

func deleteAgentWraps(ctx context.Context, x execer, userID, connID string) error {
	_, err := x.ExecContext(ctx, `DELETE FROM user_key_wraps WHERE user_id = $1 AND kind = 'agent'
		AND ref_id IN (SELECT id FROM agent_tokens WHERE connection_id = $2)`, userID, connID)
	return err
}

// OAuth wrap kinds (ref_id is the SHA-256 of the code or token).
const (
	WrapKindOAuthCode    = "oauth_code"
	WrapKindOAuthAccess  = "oauth_access"
	WrapKindOAuthRefresh = "oauth_refresh"
)

// deleteOAuthWraps deletes every code, access and refresh wrap of a
// connection's grants.
func deleteOAuthWraps(ctx context.Context, x execer, userID, connID string) error {
	_, err := x.ExecContext(ctx, `DELETE FROM user_key_wraps WHERE user_id = $1 AND (
		(kind IN ('oauth_access','oauth_refresh') AND ref_id IN (SELECT token_hash FROM oauth_tokens WHERE connection_id = $2))
		OR (kind = 'oauth_code' AND ref_id IN (SELECT code_hash FROM oauth_codes WHERE connection_id = $2)))`, userID, connID)
	return err
}

// EncState reports users.encryption_state ("none" for an unknown user).
func (st *Store) EncState(ctx context.Context, userID string) (string, error) {
	return st.encState(ctx, userID)
}

// ResolveBearer maps an agent token or OAuth access token to (user,
// connection, key). Revoked connections, deleted users and unknown tokens
// fail with ErrInvalidToken; an expired agent token with model.ErrTokenExpired;
// an encrypted user whose token holds no usable wrap (or whose connection is
// flagged needs_reconnect) with model.ErrNeedsReconnect. The returned UK is
// the caller's to Zero.
func (st *Store) ResolveBearer(ctx context.Context, token string) (userID, connID string, uk *keys.UK, err error) {
	if token == "" {
		return "", "", nil, ErrInvalidToken
	}
	h := HashToken(token)
	now := st.now()
	var tokenID string
	var expires sql.NullTime
	var needs, agent bool
	err = st.DB.QueryRowContext(ctx, `SELECT c.user_id, c.id, t.id, t.expires_at, c.needs_reconnect FROM agent_tokens t
		JOIN connections c ON c.id = t.connection_id
		JOIN users u ON u.id = c.user_id
		WHERE t.token_hash = $1 AND c.revoked_at IS NULL AND u.deleted_at IS NULL`, h).Scan(&userID, &connID, &tokenID, &expires, &needs)
	agent = err == nil
	if errors.Is(err, sql.ErrNoRows) {
		err = st.DB.QueryRowContext(ctx, `SELECT c.user_id, c.id, c.needs_reconnect FROM oauth_tokens t
			JOIN connections c ON c.id = t.connection_id
			JOIN users u ON u.id = c.user_id
			WHERE t.token_hash = $1 AND t.kind = 'access' AND t.revoked_at IS NULL AND t.expires_at > $2
			AND c.revoked_at IS NULL AND u.deleted_at IS NULL`, h, now).Scan(&userID, &connID, &needs)
		if err == nil {
			_, _ = st.DB.ExecContext(ctx, `UPDATE oauth_tokens SET last_used_at = $2 WHERE token_hash = $1`, h, now)
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil, ErrInvalidToken
	}
	if err != nil {
		return "", "", nil, fmt.Errorf("accounts: resolve token: %w", err)
	}
	if agent && expires.Valid && !expires.Time.After(now) {
		_ = st.DeleteWrapIfExpired(ctx, now, userID, wrapKindAgent, tokenID)
		return "", "", nil, model.ErrTokenExpired
	}
	state, serr := st.encState(ctx, userID)
	if serr != nil {
		return "", "", nil, serr
	}
	if needs && state != "none" {
		return "", "", nil, model.ErrNeedsReconnect
	}
	if state != "none" {
		kind, ref := wrapKindAgent, tokenID
		if !agent {
			kind, ref = WrapKindOAuthAccess, h
		}
		w, werr := st.GetWrap(ctx, userID, kind, ref)
		if werr != nil {
			if errors.Is(werr, ErrNoWrap) {
				return "", "", nil, model.ErrNeedsReconnect
			}
			return "", "", nil, werr
		}
		if uk, err = keys.Unwrap(keys.WrapKeySecret(kind, token), userID, kind, ref, w.Wrapped); err != nil {
			return "", "", nil, model.ErrNeedsReconnect
		}
	}
	_, _ = st.DB.ExecContext(ctx, `UPDATE connections SET last_used = $2 WHERE id = $1`, connID, now)
	if uk != nil {
		kind, ref := wrapKindAgent, tokenID
		if !agent {
			kind, ref = WrapKindOAuthAccess, h
		}
		_, _ = st.DB.ExecContext(ctx, `UPDATE user_key_wraps SET last_used_at = $4 WHERE user_id = $1 AND kind = $2 AND ref_id = $3`, userID, kind, ref, now)
	}
	return userID, connID, uk, nil
}

// DeleteAccount cuts access at once: it revokes every connection, deletes
// all oauth tokens, codes and sessions, and soft-deletes the user.
func (st *Store) DeleteAccount(ctx context.Context, userID string) error {
	tx, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := st.now()
	res, err := tx.ExecContext(ctx, `UPDATE users SET deleted_at = COALESCE(deleted_at, $2) WHERE id = $1`, userID, now)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	for _, q := range []string{
		`DELETE FROM user_key_wraps WHERE user_id = $1 AND kind IN ('oauth_code','oauth_access','oauth_refresh') AND $2::timestamptz IS NOT NULL`,
		`UPDATE connections SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL`,
		`DELETE FROM oauth_tokens WHERE user_id = $1 AND $2::timestamptz IS NOT NULL`,
		`DELETE FROM oauth_codes WHERE user_id = $1 AND $2::timestamptz IS NOT NULL`,
		`DELETE FROM sessions WHERE user_id = $1 AND $2::timestamptz IS NOT NULL`,
	} {
		if _, err := tx.ExecContext(ctx, q, userID, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RestoreAccount clears deleted_at. Connections stay revoked.
func (st *Store) RestoreAccount(ctx context.Context, userID string) error {
	res, err := st.DB.ExecContext(ctx, `UPDATE users SET deleted_at = NULL WHERE id = $1`, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
