package accounts

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestWrapRoundTrip(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user(t, "a")
	if _, err := e.st.GetWrap(ctx, u, "recovery", "r"); !errors.Is(err, ErrNoWrap) {
		t.Fatalf("got %v", err)
	}
	if err := e.st.PutWrap(ctx, Wrap{UserID: u, Kind: "recovery", RefID: "r", Wrapped: []byte{1, 2}}); err != nil {
		t.Fatal(err)
	}
	// upsert replaces
	if err := e.st.PutWrap(ctx, Wrap{UserID: u, Kind: "recovery", RefID: "r", Wrapped: []byte{3}, Pending: true}); err != nil {
		t.Fatal(err)
	}
	w, err := e.st.GetWrap(ctx, u, "recovery", "r")
	if err != nil || !bytes.Equal(w.Wrapped, []byte{3}) || !w.Pending {
		t.Fatalf("%+v %v", w, err)
	}
	if err := e.st.DeleteWrap(ctx, u, "recovery", "r"); err != nil {
		t.Fatal(err)
	}
	var n int
	e.st.DB.QueryRow(`SELECT count(*) FROM user_key_wraps WHERE user_id=$1`, u).Scan(&n)
	if n != 0 {
		t.Fatal("not hard deleted")
	}
}

func TestGetWrapHidesExpiredAndRevoked(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user(t, "a")
	past, fut := e.now.Add(-time.Hour), e.now.Add(time.Hour)
	e.st.PutWrap(ctx, Wrap{UserID: u, Kind: "k", RefID: "exp", Wrapped: []byte{1}, ExpiresAt: &past})
	e.st.PutWrap(ctx, Wrap{UserID: u, Kind: "k", RefID: "rev", Wrapped: []byte{1}, RevokedAt: &past})
	e.st.PutWrap(ctx, Wrap{UserID: u, Kind: "k", RefID: "ok", Wrapped: []byte{1}, ExpiresAt: &fut})
	for _, r := range []string{"exp", "rev"} {
		if _, err := e.st.GetWrap(ctx, u, "k", r); !errors.Is(err, ErrNoWrap) {
			t.Errorf("%s: %v", r, err)
		}
	}
	if _, err := e.st.GetWrap(ctx, u, "k", "ok"); err != nil {
		t.Fatal(err)
	}
}

func TestPurgeExpiredWraps(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user(t, "a")
	past, fut := e.now.Add(-time.Hour), e.now.Add(time.Hour)
	e.st.PutWrap(ctx, Wrap{UserID: u, Kind: "k", RefID: "1", Wrapped: []byte{1}, ExpiresAt: &past})
	e.st.PutWrap(ctx, Wrap{UserID: u, Kind: "k", RefID: "2", Wrapped: []byte{1}, ExpiresAt: &fut})
	e.st.PutWrap(ctx, Wrap{UserID: u, Kind: "k", RefID: "3", Wrapped: []byte{1}})
	n, err := e.st.PurgeExpiredWraps(ctx, e.now)
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
}

func TestWrapsCascadeOnUserDelete(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user(t, "a")
	e.st.PutWrap(ctx, Wrap{UserID: u, Kind: "k", RefID: "1", Wrapped: []byte{1}})
	if _, err := e.st.DB.Exec(`DELETE FROM users WHERE id=$1`, u); err != nil {
		t.Fatal(err)
	}
	var n int
	e.st.DB.QueryRow(`SELECT count(*) FROM user_key_wraps`).Scan(&n)
	if n != 0 {
		t.Fatal("wraps survived user delete")
	}
}

func TestWrapTxVariantsRollBack(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user(t, "a")
	e.st.PutWrap(ctx, Wrap{UserID: u, Kind: "k", RefID: "1", Wrapped: []byte{1}})
	tx, err := e.st.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.PutWrapTx(ctx, tx, Wrap{UserID: u, Kind: "k", RefID: "2", Wrapped: []byte{2}}); err != nil {
		t.Fatal(err)
	}
	if w, err := e.st.GetWrapTx(ctx, tx, u, "k", "2"); err != nil || w.Wrapped[0] != 2 {
		t.Fatalf("%v %v", w, err)
	}
	if err := e.st.DeleteWrapTx(ctx, tx, u, "k", "1"); err != nil {
		t.Fatal(err)
	}
	if err := e.st.DeleteWrapsByKindTx(ctx, tx, u, "zzz"); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	if _, err := e.st.GetWrap(ctx, u, "k", "1"); err != nil {
		t.Fatal("rollback did not restore")
	}
	if _, err := e.st.GetWrap(ctx, u, "k", "2"); !errors.Is(err, ErrNoWrap) {
		t.Fatal("rolled back insert visible")
	}
}

func TestDeleteWrapsExceptAndByKind(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u, v := e.user(t, "a"), e.user(t, "b")
	for _, k := range []string{"passkey", "recovery", "session", "agent"} {
		e.st.PutWrap(ctx, Wrap{UserID: u, Kind: k, RefID: "1", Wrapped: []byte{1}})
		e.st.PutWrap(ctx, Wrap{UserID: v, Kind: k, RefID: "1", Wrapped: []byte{1}})
	}
	if err := e.st.DeleteWrapsExcept(ctx, u, "passkey", "recovery"); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]bool{"passkey": true, "recovery": true, "session": false, "agent": false} {
		_, err := e.st.GetWrap(ctx, u, k, "1")
		if (err == nil) != want {
			t.Errorf("%s present=%v want %v", k, err == nil, want)
		}
	}
	if _, err := e.st.GetWrap(ctx, v, "session", "1"); err != nil {
		t.Error("other user's wrap deleted")
	}
	e.st.DeleteWrapsByKind(ctx, u, "passkey")
	if _, err := e.st.GetWrap(ctx, u, "passkey", "1"); !errors.Is(err, ErrNoWrap) {
		t.Error("by kind failed")
	}
	// no keepKinds deletes everything
	e.st.DeleteWrapsExcept(ctx, u)
	if _, err := e.st.GetWrap(ctx, u, "recovery", "1"); !errors.Is(err, ErrNoWrap) {
		t.Error("except with none kept failed")
	}
}
