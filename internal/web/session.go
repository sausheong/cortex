package web

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/sausheong/cortex/internal/accounts"
)

const (
	sessionCookie = "cortex_session"
	sessionTTL    = 30 * 24 * time.Hour
)

// ErrNoSession is returned when a session is absent, expired, or belongs to a
// deleted account.
var ErrNoSession = errors.New("web: no session")

// SessionUser is the signed-in user of a request.
type SessionUser struct{ UserID, CSRF string }

// Sessions stores browser sessions. Only the SHA-256 of the cookie value is
// stored.
type Sessions struct {
	st  *accounts.Store
	now func() time.Time
}

// NewSessions returns a session store over the accounts database.
func NewSessions(st *accounts.Store, now func() time.Time) *Sessions {
	if now == nil {
		now = time.Now
	}
	return &Sessions{st: st, now: now}
}

// Create issues a fresh session ID for userID and returns the cookie value
// and the session's CSRF token.
func (s *Sessions) Create(ctx context.Context, userID string) (id, csrf string, err error) {
	id, csrf = randToken(), randToken()
	now := s.now().UTC()
	_, err = s.st.DB.ExecContext(ctx,
		`INSERT INTO sessions (id_hash, user_id, csrf, created_at, expires_at) VALUES ($1,$2,$3,$4,$5)`,
		accounts.HashToken(id), userID, csrf, now, now.Add(sessionTTL))
	return id, csrf, err
}

// Lookup resolves a cookie value to a live session of a live account.
func (s *Sessions) Lookup(ctx context.Context, id string) (SessionUser, error) {
	if id == "" {
		return SessionUser{}, ErrNoSession
	}
	var u SessionUser
	err := s.st.DB.QueryRowContext(ctx,
		`SELECT s.user_id, s.csrf FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.id_hash = $1 AND s.expires_at > $2 AND u.deleted_at IS NULL`,
		accounts.HashToken(id), s.now().UTC()).Scan(&u.UserID, &u.CSRF)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionUser{}, ErrNoSession
	}
	return u, err
}

// Delete removes a session by cookie value.
func (s *Sessions) Delete(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	_, err := s.st.DB.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = $1`, accounts.HashToken(id))
	return err
}
