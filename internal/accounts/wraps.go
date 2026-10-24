package accounts

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrNoWrap is returned for a missing, revoked or expired wrap.
var ErrNoWrap = errors.New("accounts: no key wrap")

// Wrap is one wrapped copy of a user's data key.
type Wrap struct {
	UserID, Kind, RefID string
	Wrapped             []byte
	Pending             bool
	ExpiresAt           *time.Time
	LastUsedAt          *time.Time
	RevokedAt           *time.Time
}

// rower is satisfied by *sql.DB and *sql.Tx (execer is in connections.go).
type rower interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

const putWrapSQL = `INSERT INTO user_key_wraps (user_id, kind, ref_id, wrapped_uk, pending, expires_at, last_used_at, revoked_at)
	VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	ON CONFLICT (user_id, kind, ref_id) DO UPDATE SET
	  wrapped_uk = EXCLUDED.wrapped_uk, pending = EXCLUDED.pending, expires_at = EXCLUDED.expires_at,
	  last_used_at = EXCLUDED.last_used_at, revoked_at = EXCLUDED.revoked_at, created_at = now()`

func putWrap(ctx context.Context, x execer, w Wrap) error {
	_, err := x.ExecContext(ctx, putWrapSQL, w.UserID, w.Kind, w.RefID, w.Wrapped, w.Pending, w.ExpiresAt, w.LastUsedAt, w.RevokedAt)
	return err
}

func getWrap(ctx context.Context, x rower, now time.Time, userID, kind, refID string) (*Wrap, error) {
	w := &Wrap{UserID: userID, Kind: kind, RefID: refID}
	err := x.QueryRowContext(ctx,
		`SELECT wrapped_uk, pending, expires_at, last_used_at, revoked_at FROM user_key_wraps
		 WHERE user_id = $1 AND kind = $2 AND ref_id = $3
		   AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > $4)`,
		userID, kind, refID, now).Scan(&w.Wrapped, &w.Pending, &w.ExpiresAt, &w.LastUsedAt, &w.RevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoWrap
	}
	if err != nil {
		return nil, err
	}
	return w, nil
}

func deleteWrap(ctx context.Context, x execer, userID, kind, refID string) error {
	_, err := x.ExecContext(ctx, `DELETE FROM user_key_wraps WHERE user_id = $1 AND kind = $2 AND ref_id = $3`, userID, kind, refID)
	return err
}

func deleteWrapsByKind(ctx context.Context, x execer, userID, kind string) error {
	_, err := x.ExecContext(ctx, `DELETE FROM user_key_wraps WHERE user_id = $1 AND kind = $2`, userID, kind)
	return err
}

// PutWrap upserts a wrap, replacing any existing one with the same key.
func (st *Store) PutWrap(ctx context.Context, w Wrap) error { return putWrap(ctx, st.DB, w) }

// PutWrapTx is PutWrap inside tx.
func (st *Store) PutWrapTx(ctx context.Context, tx *sql.Tx, w Wrap) error {
	return putWrap(ctx, tx, w)
}

// GetWrap returns ErrNoWrap for missing, revoked or expired rows.
func (st *Store) GetWrap(ctx context.Context, userID, kind, refID string) (*Wrap, error) {
	return getWrap(ctx, st.DB, st.now(), userID, kind, refID)
}

// GetWrapTx is GetWrap inside tx.
func (st *Store) GetWrapTx(ctx context.Context, tx *sql.Tx, userID, kind, refID string) (*Wrap, error) {
	return getWrap(ctx, tx, st.now(), userID, kind, refID)
}

// DeleteWrap hard-deletes one wrap. Missing rows are not an error.
func (st *Store) DeleteWrap(ctx context.Context, userID, kind, refID string) error {
	return deleteWrap(ctx, st.DB, userID, kind, refID)
}

// DeleteWrapTx is DeleteWrap inside tx.
func (st *Store) DeleteWrapTx(ctx context.Context, tx *sql.Tx, userID, kind, refID string) error {
	return deleteWrap(ctx, tx, userID, kind, refID)
}

// DeleteWrapsByKind hard-deletes every wrap of a kind for the user.
func (st *Store) DeleteWrapsByKind(ctx context.Context, userID, kind string) error {
	return deleteWrapsByKind(ctx, st.DB, userID, kind)
}

// DeleteWrapsByKindTx is DeleteWrapsByKind inside tx.
func (st *Store) DeleteWrapsByKindTx(ctx context.Context, tx *sql.Tx, userID, kind string) error {
	return deleteWrapsByKind(ctx, tx, userID, kind)
}

// DeleteWrapsExcept hard-deletes all the user's wraps except those of keepKinds.
func (st *Store) DeleteWrapsExcept(ctx context.Context, userID string, keepKinds ...string) error {
	if keepKinds == nil {
		keepKinds = []string{}
	}
	_, err := st.DB.ExecContext(ctx,
		`DELETE FROM user_key_wraps WHERE user_id = $1 AND NOT (kind = ANY($2::text[]))`, userID, keepKinds)
	return err
}

// PurgeExpiredWraps hard-deletes wraps whose expiry has passed.
func (st *Store) PurgeExpiredWraps(ctx context.Context, now time.Time) (int, error) {
	r, err := st.DB.ExecContext(ctx, `DELETE FROM user_key_wraps WHERE expires_at IS NOT NULL AND expires_at <= $1`, now)
	if err != nil {
		return 0, err
	}
	n, err := r.RowsAffected()
	// Session wraps whose session row is gone (logout races, cascade-less
	// purges) hold key material for nobody.
	r2, err2 := st.DB.ExecContext(ctx,
		`DELETE FROM user_key_wraps w WHERE w.kind = 'session'
		 AND NOT EXISTS (SELECT 1 FROM sessions s WHERE s.id_hash = w.ref_id)`)
	if err == nil {
		err = err2
	}
	if err2 == nil {
		m, _ := r2.RowsAffected()
		n += m
	}
	return int(n), err
}

// GetWrapAt is GetWrap judged against an explicit clock (web sessions carry
// their own).
func (st *Store) GetWrapAt(ctx context.Context, now time.Time, userID, kind, refID string) (*Wrap, error) {
	return getWrap(ctx, st.DB, now, userID, kind, refID)
}

// DeleteWrapIfExpired hard-deletes one wrap whose expiry has passed.
func (st *Store) DeleteWrapIfExpired(ctx context.Context, now time.Time, userID, kind, refID string) error {
	_, err := st.DB.ExecContext(ctx,
		`DELETE FROM user_key_wraps WHERE user_id = $1 AND kind = $2 AND ref_id = $3
		 AND expires_at IS NOT NULL AND expires_at <= $4`, userID, kind, refID, now)
	return err
}

// ExtendWrap slides a wrap's expiry to newExpiry and stamps last_used_at in one
// UPDATE, at most once per minGap (rows touched more recently are left alone).
func (st *Store) ExtendWrap(ctx context.Context, now time.Time, userID, kind, refID string, newExpiry time.Time, minGap time.Duration) error {
	_, err := st.DB.ExecContext(ctx,
		`UPDATE user_key_wraps SET last_used_at = $5, expires_at = $6
		 WHERE user_id = $1 AND kind = $2 AND ref_id = $3 AND pending = false
		   AND (last_used_at IS NULL OR last_used_at <= $4)`,
		userID, kind, refID, now.Add(-minGap), now, newExpiry)
	return err
}

// DeleteSessionWrapsExcept hard-deletes the user's session wraps except the one
// bound to the session with id_hash keepSessionHash (empty deletes them all).
// "Forget all devices" calls it with the current session's hash.
func (st *Store) DeleteSessionWrapsExcept(ctx context.Context, userID, keepSessionHash string) error {
	_, err := st.DB.ExecContext(ctx,
		`DELETE FROM user_key_wraps WHERE user_id = $1 AND kind = 'session' AND ref_id <> $2`, userID, keepSessionHash)
	return err
}
