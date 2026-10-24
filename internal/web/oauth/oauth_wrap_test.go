package oauth

import (
	"errors"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/web"
)

// encUser creates an encrypted user whose session is unlocked.
func (e *env) encUser(sub string) (sess, *keys.UK) {
	e.t.Helper()
	s := e.user(sub)
	if _, err := e.st.DB.Exec(`UPDATE users SET encryption_state = 'encrypted' WHERE id = $1`, s.userID); err != nil {
		e.t.Fatal(err)
	}
	uk, err := keys.NewUK()
	if err != nil {
		e.t.Fatal(err)
	}
	secret, _, err := web_sessions(e).Unlock(e.t.Context(), s.id, uk)
	if err != nil {
		e.t.Fatal(err)
	}
	s.unlock = secret
	return s, uk
}

func (e *env) wraps(kind string) int {
	e.t.Helper()
	var n int
	if err := e.st.DB.QueryRow(`SELECT count(*) FROM user_key_wraps WHERE kind = $1`, kind).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) oauthWraps() int {
	return e.wraps("oauth_code") + e.wraps("oauth_access") + e.wraps("oauth_refresh")
}

func sameKey(t *testing.T, userID string, a, b *keys.UK) {
	t.Helper()
	ct, err := a.Seal(userID, "t", "r", "c", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := b.Open(userID, "t", "r", "c", ct)
	if err != nil || string(pt) != "hello" {
		t.Fatalf("keys differ: %v", err)
	}
}

func TestOAuthCodeWrapsUKAndIsDeletedOnExchange(t *testing.T) {
	e := newEnv(t, Options{})
	s, _ := e.encUser("a")
	id := e.register("App", cb)
	rid, _ := e.consent(&s, id, cb)
	code := codeFrom(t, e.allow(&s, rid))
	if n := e.wraps("oauth_code"); n != 1 {
		t.Fatalf("code wraps %d", n)
	}
	if resp, _ := e.exchange(code, id, cb); resp.StatusCode != 200 {
		t.Fatalf("exchange %d", resp.StatusCode)
	}
	if e.wraps("oauth_code") != 0 || e.wraps("oauth_access") != 1 || e.wraps("oauth_refresh") != 1 {
		t.Fatalf("wraps code=%d access=%d refresh=%d", e.wraps("oauth_code"), e.wraps("oauth_access"), e.wraps("oauth_refresh"))
	}
}

func TestOAuthTokenResolvesToUserUK(t *testing.T) {
	e := newEnv(t, Options{})
	s, uk := e.encUser("a")
	tk := e.connect(&s, e.register("App", cb))
	uid, _, got, err := e.st.ResolveBearer(t.Context(), tk.Access)
	if err != nil || uid != s.userID || got == nil {
		t.Fatalf("resolve: %v %v", uid, err)
	}
	defer got.Zero()
	sameKey(t, s.userID, uk, got)
}

func TestOAuthRefreshRewrapsAndDeletesOldWraps(t *testing.T) {
	e := newEnv(t, Options{})
	s, uk := e.encUser("a")
	id := e.register("App", cb)
	tk := e.connect(&s, id)
	resp, t2 := e.refresh(tk.Refresh, id)
	if resp.StatusCode != 200 {
		t.Fatalf("refresh %d", resp.StatusCode)
	}
	if n := e.wraps("oauth_access") + e.wraps("oauth_refresh"); n != 2 {
		t.Fatalf("wraps %d", n)
	}
	if _, _, _, err := e.st.ResolveBearer(t.Context(), tk.Access); err == nil {
		t.Fatal("old access still resolves")
	}
	_, _, got, err := e.st.ResolveBearer(t.Context(), t2.Access)
	if err != nil {
		t.Fatal(err)
	}
	sameKey(t, s.userID, uk, got)
	got.Zero()
	if r, _ := e.refresh(tk.Refresh, id); r.StatusCode != 400 {
		t.Fatalf("old refresh: %d", r.StatusCode)
	}
}

func TestOAuthRefreshReuseRevokesAndDeletesWraps(t *testing.T) {
	e := newEnv(t, Options{})
	s, _ := e.encUser("a")
	id := e.register("App", cb)
	tk := e.connect(&s, id)
	e.refresh(tk.Refresh, id)
	if r, _ := e.refresh(tk.Refresh, id); r.StatusCode != 400 {
		t.Fatalf("reuse %d", r.StatusCode)
	}
	if n := e.oauthWraps(); n != 0 {
		t.Fatalf("wraps left: %d", n)
	}
}

func TestOAuthCodeReplayLeavesNoWraps(t *testing.T) {
	e := newEnv(t, Options{})
	s, _ := e.encUser("a")
	id := e.register("App", cb)
	rid, _ := e.consent(&s, id, cb)
	code := codeFrom(t, e.allow(&s, rid))
	e.exchange(code, id, cb)
	if r, _ := e.exchange(code, id, cb); r.StatusCode != 400 {
		t.Fatalf("replay %d", r.StatusCode)
	}
	if n := e.oauthWraps(); n != 0 {
		t.Fatalf("wraps left: %d", n)
	}
}

func TestRevokeClientDeletesWraps(t *testing.T) {
	e := newEnv(t, Options{})
	s, _ := e.encUser("a")
	e.connect(&s, e.register("App", cb))
	var connID string
	if err := e.st.DB.QueryRow(`SELECT id FROM connections WHERE user_id = $1 AND kind = 'oauth'`, s.userID).Scan(&connID); err != nil {
		t.Fatal(err)
	}
	if e.oauthWraps() != 2 {
		t.Fatalf("setup wraps %d", e.oauthWraps())
	}
	if err := e.st.RevokeConnection(t.Context(), s.userID, connID); err != nil {
		t.Fatal(err)
	}
	if n := e.oauthWraps(); n != 0 {
		t.Fatalf("wraps left: %d", n)
	}
}

func TestExpiredAccessWrapNotUsable(t *testing.T) {
	e := newEnv(t, Options{})
	s, _ := e.encUser("a")
	tk := e.connect(&s, e.register("App", cb))
	if _, err := e.st.DB.Exec(`UPDATE user_key_wraps SET expires_at = $1 WHERE kind = 'oauth_access'`, e.now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := e.st.ResolveBearer(t.Context(), tk.Access); !errors.Is(err, model.ErrNeedsReconnect) {
		t.Fatalf("got %v", err)
	}
}

func TestAuthorizeNeedsUnlockForEncryptedUser(t *testing.T) {
	e := newEnv(t, Options{})
	s, _ := e.encUser("a")
	s.unlock = "" // signed in, locked
	id := e.register("App", cb)
	resp, _ := e.get(authQuery(id, cb), &s)
	if resp.StatusCode != 303 || !strings.HasPrefix(resp.Header.Get("Location"), "/unlock?next=") {
		t.Fatalf("GET: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, _ = e.postForm("/oauth/authorize", &s, url.Values{"request_id": {"x"}, "decision": {"allow"}})
	if resp.StatusCode != 423 {
		t.Fatalf("POST: %d", resp.StatusCode)
	}
	if e.oauthWraps() != 0 {
		t.Fatal("wraps written while locked")
	}
}

func TestPkceMismatchDeletesCodeWrap(t *testing.T) {
	e := newEnv(t, Options{})
	s, _ := e.encUser("a")
	id := e.register("App", cb)
	rid, _ := e.consent(&s, id, cb)
	code := codeFrom(t, e.allow(&s, rid))
	resp, _ := e.postForm("/oauth/token", nil, url.Values{"grant_type": {"authorization_code"}, "code": {code},
		"client_id": {id}, "redirect_uri": {cb}, "code_verifier": {strings.Repeat("x", 50)}})
	if resp.StatusCode != 400 {
		t.Fatalf("mismatch %d", resp.StatusCode)
	}
	if n := e.wraps("oauth_code"); n != 0 {
		t.Fatalf("code wrap survived: %d", n)
	}
	if r, _ := e.exchange(code, id, cb); r.StatusCode != 400 {
		t.Fatalf("burned code worked: %d", r.StatusCode)
	}
}

func TestRefreshRotationRollbackKeepsOldWraps(t *testing.T) {
	var fail atomic.Bool
	e := newEnv(t, Options{FailAfterRewrap: func() error {
		if fail.Load() {
			return errors.New("injected")
		}
		return nil
	}})
	s, _ := e.encUser("a")
	id := e.register("App", cb)
	tk := e.connect(&s, id)
	fail.Store(true)
	if r, _ := e.refresh(tk.Refresh, id); r.StatusCode != 500 {
		t.Fatalf("expected 500, got %d", r.StatusCode)
	}
	var n int
	e.st.DB.QueryRow(`SELECT count(*) FROM user_key_wraps WHERE kind IN ('oauth_access','oauth_refresh') AND ref_id IN ($1,$2)`,
		accounts.HashToken(tk.Access), accounts.HashToken(tk.Refresh)).Scan(&n)
	if n != 2 || e.oauthWraps() != 2 {
		t.Fatalf("old wraps %d, total %d", n, e.oauthWraps())
	}
	fail.Store(false)
	if r, _ := e.refresh(tk.Refresh, id); r.StatusCode != 200 {
		t.Fatalf("old refresh should still work: %d", r.StatusCode)
	}
}

func TestConcurrentRefreshSingleWinner(t *testing.T) {
	e := newEnv(t, Options{})
	s, _ := e.encUser("a")
	id := e.register("App", cb)
	tk := e.connect(&s, id)
	var wg sync.WaitGroup
	var ok atomic.Int32
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r, _ := e.refresh(tk.Refresh, id); r.StatusCode == 200 {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 {
		t.Fatalf("winners: %d", ok.Load())
	}
	// A losing duplicate is a reuse and revokes the grant, so the wrap count
	// must equal the number of live tokens (never a leaked or half pair).
	var live int
	e.st.DB.QueryRow(`SELECT count(*) FROM oauth_tokens WHERE revoked_at IS NULL`).Scan(&live)
	if got := e.oauthWraps(); got != live {
		t.Fatalf("wraps %d != live tokens %d", got, live)
	}
}

func web_sessions(e *env) *web.Sessions {
	return web.NewSessions(e.st, func() time.Time { return e.now })
}

// An OAuth connection flagged to reconnect after enrollment works again once
// the user consents anew: the same connection is re-keyed and un-flagged.
func TestOAuthReconnectClearsNeedsReconnect(t *testing.T) {
	e := newEnv(t, Options{})
	s, uk := e.encUser("a")
	id := e.register("App", cb)
	first := e.connect(&s, id)
	if _, err := e.st.DB.Exec(`UPDATE connections SET needs_reconnect = TRUE WHERE user_id = $1`, s.userID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := e.st.ResolveBearer(t.Context(), first.Access); !errors.Is(err, model.ErrNeedsReconnect) {
		t.Fatalf("flagged connection: %v", err)
	}
	again := e.connect(&s, id)
	_, _, got, err := e.st.ResolveBearer(t.Context(), again.Access)
	if err != nil {
		t.Fatalf("after re-consent: %v", err)
	}
	sameKey(t, s.userID, uk, got)
	got.Zero()
}
