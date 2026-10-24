package accounts

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Enrollment cohorts and key management for the Account page.

// EnrollInfo is a user's encryption state.
type EnrollInfo struct {
	State string
}

// EnrollInfo reads the state. An unknown user is "none".
func (st *Store) EnrollInfo(ctx context.Context, userID string) (EnrollInfo, error) {
	var i EnrollInfo
	err := st.DB.QueryRowContext(ctx,
		`SELECT encryption_state FROM users WHERE id = $1`, userID).Scan(&i.State)
	if errors.Is(err, sql.ErrNoRows) {
		return EnrollInfo{State: "none"}, nil
	}
	return i, err
}

// EnrollRequired reports whether userID must enroll before storing or
// connecting anything: enrollment is switched on and the account is not yet
// protected. While enrollment is off nobody can comply, so the page does not
// ask (content calls still fail with model.ErrEnrollRequired).
func (st *Store) EnrollRequired(ctx context.Context, userID string) (bool, error) {
	if !st.EnrollEnabled() {
		return false, nil
	}
	i, err := st.EnrollInfo(ctx, userID)
	if err != nil {
		return false, err
	}
	return i.State == "none", nil
}

// MarkNeedsReconnect flags every live connection of the user. Their tokens
// were issued without a key wrap, so after enrollment they cannot work until
// the user reconnects them.
func (st *Store) MarkNeedsReconnect(ctx context.Context, userID string) error {
	return markNeedsReconnect(ctx, st.DB, userID)
}

func markNeedsReconnect(ctx context.Context, x execer, userID string) error {
	_, err := x.ExecContext(ctx,
		`UPDATE connections SET needs_reconnect = TRUE WHERE user_id = $1 AND revoked_at IS NULL AND removed_at IS NULL`, userID)
	return err
}

// ForgetDevices cuts every other browser and OAuth assistant loose: it
// deletes the session wraps except keepSessionHash (the caller's own, so the
// caller stays unlocked; empty keeps none), deletes OAuth code, access and
// refresh wraps, revokes the OAuth tokens and flags the OAuth connections to
// reconnect. Agent tokens and their wraps are untouched.
func (st *Store) ForgetDevices(ctx context.Context, userID, keepSessionHash string) error {
	state, _, err := st.userState(ctx, st.DB, userID)
	if err != nil {
		return err
	}
	if state == "none" {
		return ErrNotEnrolled
	}
	tx, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM user_key_wraps WHERE user_id = $1 AND kind = 'session' AND ref_id <> $2`, userID, keepSessionHash); err != nil {
		return err
	}
	for _, q := range []string{
		`DELETE FROM user_key_wraps WHERE user_id = $1 AND kind IN ('oauth_code','oauth_access','oauth_refresh')`,
		`UPDATE oauth_tokens SET revoked_at = COALESCE(revoked_at, now()) WHERE user_id = $1`,
		`UPDATE connections SET needs_reconnect = TRUE
		   WHERE user_id = $1 AND kind = 'oauth' AND revoked_at IS NULL AND removed_at IS NULL`,
	} {
		if _, err := tx.ExecContext(ctx, q, userID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PasskeyInfo is a registered passkey as the Account page lists it.
type PasskeyInfo struct {
	ID, Name  string
	CreatedAt time.Time
	LastUsed  *time.Time
}

// ListPasskeys returns the user's confirmed passkeys, oldest first.
func (st *Store) ListPasskeys(ctx context.Context, userID string) ([]PasskeyInfo, error) {
	rows, err := st.DB.QueryContext(ctx,
		`SELECT c.id, c.name, c.created_at, c.last_used_at
		 FROM webauthn_credentials c
		 JOIN user_key_wraps w ON w.user_id = c.user_id AND w.kind = 'passkey' AND w.ref_id = c.id
		 WHERE c.user_id = $1 AND NOT w.pending
		 ORDER BY c.created_at, c.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PasskeyInfo
	for rows.Next() {
		var p PasskeyInfo
		var lu sql.NullTime
		if err := rows.Scan(&p.ID, &p.Name, &p.CreatedAt, &lu); err != nil {
			return nil, err
		}
		if lu.Valid {
			t := lu.Time
			p.LastUsed = &t
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
