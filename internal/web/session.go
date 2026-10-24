package web

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/keys"
)

const (
	sessionCookie = "cortex_session"
	sessionTTL    = 30 * 24 * time.Hour
)

// ErrNoSession is returned when a session is absent, expired, or belongs to a
// deleted account.
var ErrNoSession = errors.New("web: no session")

// SessionUser is the signed-in user of a request.
type SessionUser struct {
	UserID, CSRF string
	// Unlocked is true when the request carries a valid unlock cookie whose
	// session wrap opened. Key is then a per-request UK (nil when locked or
	// legacy); auth zeroes it when the handler returns, so handlers that
	// outlive the request must Clone it first.
	Unlocked bool
	Key      *keys.UK
}

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

// wrapIdle is how long an unlocked session may sit idle before it locks.
const (
	wrapIdle     = 12 * time.Hour
	wrapTouchGap = time.Minute
	unlockCookie = "cortex_unlock"
)

// Lookup resolves a cookie value to a live session of a live account. With a
// non-empty unlockSecret it also tries the session's key wrap: on success the
// user is Unlocked and Key holds a fresh UK the caller must Zero. Any failure
// (no wrap, wrong secret, wrap expired) leaves the session valid but locked.
// Activity extends the wrap to min(session expiry, now+12h), at most once a
// minute, in the same UPDATE as last_used_at.
func (s *Sessions) Lookup(ctx context.Context, id, unlockSecret string) (SessionUser, error) {
	if id == "" {
		return SessionUser{}, ErrNoSession
	}
	var (
		u       SessionUser
		exp     time.Time
		deleted sql.NullTime
	)
	h := accounts.HashToken(id)
	err := s.st.DB.QueryRowContext(ctx,
		`SELECT s.user_id, s.csrf, s.expires_at, u.deleted_at FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.id_hash = $1`, h).Scan(&u.UserID, &u.CSRF, &exp, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionUser{}, ErrNoSession
	}
	if err != nil {
		return SessionUser{}, err
	}
	now := s.now().UTC()
	if !exp.After(now) {
		_ = s.Delete(ctx, id) // also deletes the wrap
		return SessionUser{}, ErrNoSession
	}
	if deleted.Valid {
		return SessionUser{}, ErrNoSession
	}
	if unlockSecret == "" {
		return u, nil
	}
	w, err := s.st.GetWrapAt(ctx, now, u.UserID, "session", h)
	if err != nil {
		if errors.Is(err, accounts.ErrNoWrap) {
			_ = s.st.DeleteWrapIfExpired(ctx, now, u.UserID, "session", h)
			return u, nil
		}
		return SessionUser{}, err
	}
	uk, err := keys.Unwrap(keys.WrapKeySecret("session", unlockSecret), u.UserID, "session", h, w.Wrapped)
	if err != nil {
		return u, nil
	}
	_ = s.st.ExtendWrap(ctx, now, u.UserID, "session", h, minTime(exp, now.Add(wrapIdle)), wrapTouchGap)
	u.Unlocked, u.Key = true, uk
	return u, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// Unlock wraps uk under a fresh random secret bound to the session (ref_id is
// the session id_hash, part of the wrap AAD) and returns the secret for the
// cortex_unlock cookie plus the session's remaining life, which is the
// cookie's Max-Age. The caller keeps ownership of uk.
func (s *Sessions) Unlock(ctx context.Context, sessionID string, uk *keys.UK) (secret string, remaining time.Duration, err error) {
	h := accounts.HashToken(sessionID)
	var userID string
	var exp time.Time
	err = s.st.DB.QueryRowContext(ctx, `SELECT user_id, expires_at FROM sessions WHERE id_hash = $1`, h).Scan(&userID, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, ErrNoSession
	}
	if err != nil {
		return "", 0, err
	}
	now := s.now().UTC()
	if !exp.After(now) {
		return "", 0, ErrNoSession
	}
	secret = randToken()
	wrapped, err := keys.Wrap(keys.WrapKeySecret("session", secret), userID, "session", h, uk)
	if err != nil {
		return "", 0, err
	}
	err = s.st.PutWrap(ctx, accounts.Wrap{UserID: userID, Kind: "session", RefID: h, Wrapped: wrapped,
		ExpiresAt: ptr(minTime(exp, now.Add(wrapIdle))), LastUsedAt: ptr(now)})
	if err != nil {
		return "", 0, err
	}
	return secret, exp.Sub(now), nil
}

func ptr[T any](v T) *T { return &v }

// Delete removes a session and its key wrap by cookie value.
func (s *Sessions) Delete(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	h := accounts.HashToken(id)
	if _, err := s.st.DB.ExecContext(ctx, `DELETE FROM user_key_wraps WHERE kind = 'session' AND ref_id = $1`, h); err != nil {
		return err
	}
	_, err := s.st.DB.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = $1`, h)
	return err
}
