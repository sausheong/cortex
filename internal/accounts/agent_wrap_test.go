package accounts

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/model"
)

func (e *env) encrypted(t *testing.T, sub string) (string, *keys.UK) {
	t.Helper()
	u := e.user(t, sub)
	if _, err := e.st.DB.ExecContext(context.Background(), `UPDATE users SET encryption_state='encrypted' WHERE id=$1`, u); err != nil {
		t.Fatal(err)
	}
	uk, err := keys.NewUK()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(uk.Zero)
	return u, uk
}

func TestAgentTokenWrapsUKAndResolves(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u, uk := e.encrypted(t, "w1")
	tok, conn, err := e.st.CreateAgentToken(ctx, u, "a", uk)
	if err != nil {
		t.Fatal(err)
	}
	uid, cid, got, err := e.st.ResolveBearer(ctx, tok)
	if err != nil || uid != u || cid != conn.ID || got == nil {
		t.Fatalf("resolve: %v %v %v", uid, cid, err)
	}
	defer got.Zero()
	ct, err := uk.Seal(u, "t", "r", "c", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := got.Open(u, "t", "r", "c", ct)
	if err != nil || string(pt) != "hello" {
		t.Fatalf("unwrapped UK differs: %v", err)
	}
}

func TestAgentTokenCreateNeedsUnlockedKey(t *testing.T) {
	e := newEnv(t)
	u, _ := e.encrypted(t, "w2")
	if _, _, err := e.st.CreateAgentToken(context.Background(), u, "a", nil); !errors.Is(err, model.ErrLocked) {
		t.Fatalf("got %v", err)
	}
}

func TestAgentTokenExpiresAfter90Days(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u, uk := e.encrypted(t, "w3")
	tok, _, err := e.st.CreateAgentToken(ctx, u, "a", uk)
	if err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(89 * 24 * time.Hour)
	if _, _, k, err := e.st.ResolveBearer(ctx, tok); err != nil {
		t.Fatalf("day 89: %v", err)
	} else {
		k.Zero()
	}
	e.now = e.now.Add(2 * 24 * time.Hour)
	if _, _, _, err := e.st.ResolveBearer(ctx, tok); !errors.Is(err, model.ErrTokenExpired) {
		t.Fatalf("day 91: %v", err)
	}
	var n int
	e.st.DB.QueryRowContext(ctx, `SELECT count(*) FROM user_key_wraps WHERE user_id=$1 AND kind='agent'`, u).Scan(&n)
	if n != 0 {
		t.Fatalf("expired wrap kept: %d", n)
	}
}

func TestRenewAgentTokenReplacesOldToken(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u, uk := e.encrypted(t, "w4")
	old, conn, _ := e.st.CreateAgentToken(ctx, u, "a", uk)
	e.now = e.now.Add(60 * 24 * time.Hour)
	nt, err := e.st.RenewAgentToken(ctx, u, conn.ID, uk)
	if err != nil || nt == old {
		t.Fatalf("renew: %v", err)
	}
	if _, _, _, err := e.st.ResolveBearer(ctx, old); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("old token: %v", err)
	}
	e.now = e.now.Add(60 * 24 * time.Hour) // day 120: past old expiry, inside new
	_, cid, k, err := e.st.ResolveBearer(ctx, nt)
	if err != nil || cid != conn.ID {
		t.Fatalf("new token: %v", err)
	}
	k.Zero()
	if _, err := e.st.RenewAgentToken(ctx, u, conn.ID, nil); !errors.Is(err, model.ErrLocked) {
		t.Fatalf("renew without key: %v", err)
	}
	if _, err := e.st.RenewAgentToken(ctx, e.user(t, "other"), conn.ID, uk); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign renew: %v", err)
	}
}

func TestRevokedConnectionTokenHasNoWrap(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u, uk := e.encrypted(t, "w5")
	tok, conn, _ := e.st.CreateAgentToken(ctx, u, "a", uk)
	if err := e.st.RevokeConnection(ctx, u, conn.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	e.st.DB.QueryRowContext(ctx, `SELECT count(*) FROM user_key_wraps WHERE user_id=$1 AND kind='agent'`, u).Scan(&n)
	if n != 0 {
		t.Fatalf("wrap survives revoke: %d", n)
	}
	if _, _, _, err := e.st.ResolveBearer(ctx, tok); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("got %v", err)
	}
}

func TestNeedsReconnectError(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user(t, "w6")
	tok, conn, _ := e.st.CreateAgentToken(ctx, u, "a", nil) // legacy token, no wrap
	e.st.DB.ExecContext(ctx, `UPDATE users SET encryption_state='encrypted' WHERE id=$1`, u)
	_, _, _, err := e.st.ResolveBearer(ctx, tok)
	if !errors.Is(err, model.ErrNeedsReconnect) || err.Error() != "cortex is now encrypted; reconnect this assistant in Connections" {
		t.Fatalf("got %v", err)
	}
	// Flagged connection also needs reconnect even with a wrap present.
	uk, _ := keys.NewUK()
	defer uk.Zero()
	tok2, c2, _ := e.st.CreateAgentToken(ctx, u, "b", uk)
	e.st.DB.ExecContext(ctx, `UPDATE connections SET needs_reconnect=TRUE WHERE id=$1`, c2.ID)
	if _, _, _, err := e.st.ResolveBearer(ctx, tok2); !errors.Is(err, model.ErrNeedsReconnect) {
		t.Fatalf("flagged: %v", err)
	}
	cs, _ := e.st.ListConnections(ctx, u)
	flagged := false
	for _, c := range cs {
		if c.ID == c2.ID && c.NeedsReconnect && c.ExpiresAt != nil {
			flagged = true
		}
		_ = conn
	}
	if !flagged {
		t.Fatal("list lacks NeedsReconnect/ExpiresAt")
	}
}

func TestExpiredOrRevokedWrapsRejected(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u, uk := e.encrypted(t, "w7")
	tok, _, _ := e.st.CreateAgentToken(ctx, u, "a", uk)
	for _, q := range []string{
		`UPDATE user_key_wraps SET expires_at = $2 WHERE user_id=$1 AND kind='agent'`,
		`UPDATE user_key_wraps SET revoked_at = $2 WHERE user_id=$1 AND kind='agent'`,
	} {
		e.st.DB.ExecContext(ctx, `UPDATE user_key_wraps SET expires_at=NULL, revoked_at=NULL WHERE user_id=$1`, u)
		e.st.DB.ExecContext(ctx, q, u, e.now.Add(-time.Hour))
		if _, _, _, err := e.st.ResolveBearer(ctx, tok); !errors.Is(err, model.ErrNeedsReconnect) {
			t.Fatalf("%s: %v", q, err)
		}
	}
}
