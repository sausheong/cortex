package web

// Session wrap and unlock cookie tests.
//
// The web tests run over httptest.NewUnstartedServer(...).StartTLS (see
// newEnv), so the Secure cookies the server sets are stored and replayed by
// the client jar. Where a test needs a precise cookie state it sets or drops
// cookies in the jar by hand, or calls Sessions directly with an injected
// clock (env.now drives both the server and the accounts store).

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/keys"
)

func (e *env) cookieVal(name string) string {
	u, _ := url.Parse(e.ts.URL)
	for _, c := range e.jar.Cookies(u) {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

func (e *env) setCookie(name, val string) {
	u, _ := url.Parse(e.ts.URL)
	e.jar.SetCookies(u, []*http.Cookie{{Name: name, Value: val, Path: "/", Secure: true}})
}

func (e *env) dropCookie(name string) {
	u, _ := url.Parse(e.ts.URL)
	e.jar.SetCookies(u, []*http.Cookie{{Name: name, Value: "", Path: "/", MaxAge: -1, Secure: true}})
}

func (e *env) wrapCount(kind string) int {
	e.t.Helper()
	var n int
	if err := e.st.DB.QueryRow(`SELECT count(*) FROM user_key_wraps WHERE kind = $1`, kind).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// encryptedSession signs alice in, marks her encrypted and unlocks the session
// directly. It returns the session id, the unlock secret and the UK.
func encryptedSession(t *testing.T) (e *env, sid, secret string, uk *keys.UK) {
	t.Helper()
	e = newEnv(t)
	e.signIn(alice(), "")
	uid := e.userBySub("g-alice").ID
	if _, err := e.st.DB.Exec(`UPDATE users SET encryption_state = 'encrypted' WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	// Enrollment (Task 16) encrypts the plaintext owner entity CreateUser
	// writes; this fixture skips enrollment, so drop it: the row-form guard
	// rejects it when a keyed page loads the working set.
	for _, q := range []string{`UPDATE users SET owner_entity_id = NULL WHERE id = $1`, `DELETE FROM entities WHERE user_id = $1 AND enc IS NOT TRUE`} {
		if _, err := e.st.DB.Exec(q, uid); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	if uk, err = keys.NewUK(); err != nil {
		t.Fatal(err)
	}
	sid = e.cookieVal(sessionCookie)
	secret, _, err = e.srv.sessions.Unlock(t.Context(), sid, uk)
	if err != nil {
		t.Fatal(err)
	}
	return
}

func TestUnlockSetsSecondCookieAndWrap(t *testing.T) {
	p := newPKWeb(t)
	en := p.startEnroll(t)
	resp, body := p.confirm(t, en, nil, en.groups())
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	var ck *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == unlockCookie {
			ck = c
		}
	}
	if ck == nil || ck.Value == "" {
		t.Fatal("no unlock cookie")
	}
	if !ck.HttpOnly || !ck.Secure || ck.SameSite != http.SameSiteLaxMode || ck.Path != "/" {
		t.Fatalf("cookie flags: %+v", ck)
	}
	if unlockCookie == sessionCookie || ck.Value == p.e.cookieVal(sessionCookie) {
		t.Fatal("unlock cookie is not separate from the session cookie")
	}
	if ck.MaxAge <= 0 || ck.MaxAge > int(sessionTTL.Seconds()) {
		t.Fatalf("Max-Age %d exceeds the session life", ck.MaxAge)
	}
	if p.e.wrapCount("session") != 1 {
		t.Fatal("no session wrap")
	}
	if r, b := p.e.get("/memories"); r.StatusCode != 200 {
		t.Fatalf("unlocked /memories: %d %.100s", r.StatusCode, b)
	}
}

func TestUnlockCookieMaxAgeIsRemainingSessionLife(t *testing.T) {
	e, _, _, _ := encryptedSession(t)
	e.now = e.now.Add(10 * 24 * time.Hour)
	uk, _ := keys.NewUK()
	sid := e.cookieVal(sessionCookie)
	_, rem, err := e.srv.sessions.Unlock(t.Context(), sid, uk)
	if err != nil {
		t.Fatal(err)
	}
	if want := sessionTTL - 10*24*time.Hour; rem > want || rem < want-time.Minute {
		t.Fatalf("remaining %v, want about %v", rem, want)
	}
}

func TestLogoutClearsUnlockCookie(t *testing.T) {
	p := newPKWeb(t)
	p.enrollAndConfirm(t)
	resp, _ := p.e.post("/logout", url.Values{"csrf": {p.csrf}})
	var ck *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == unlockCookie {
			ck = c
		}
	}
	if ck == nil || ck.MaxAge != -1 || !ck.HttpOnly || !ck.Secure || ck.SameSite != http.SameSiteLaxMode || ck.Path != "/" {
		t.Fatalf("logout cookie: %+v", ck)
	}
	if p.e.wrapCount("session") != 0 {
		t.Fatal("session wrap survived logout")
	}
}

func TestSessionWrapDeletedWhenExpiredOnLookup(t *testing.T) {
	e, sid, secret, uk := encryptedSession(t)
	defer uk.Zero()
	e.now = e.now.Add(sessionTTL + time.Minute)
	if _, err := e.srv.sessions.Lookup(t.Context(), sid, secret); err != ErrNoSession {
		t.Fatalf("expired session: %v", err)
	}
	if e.wrapCount("session") != 0 {
		t.Fatal("wrap survived an expired session")
	}
}

func lookup(t *testing.T, e *env, sid, secret string) SessionUser {
	t.Helper()
	u, err := e.srv.sessions.Lookup(t.Context(), sid, secret)
	if err != nil {
		t.Fatal(err)
	}
	if u.Key != nil {
		t.Cleanup(u.Key.Zero)
	}
	return u
}

func TestActivityAtHour11KeepsUnlocked(t *testing.T) {
	e, sid, secret, uk := encryptedSession(t)
	defer uk.Zero()
	e.now = e.now.Add(11 * time.Hour)
	if u := lookup(t, e, sid, secret); !u.Unlocked || !u.Key.Equal(uk) {
		t.Fatal("locked at hour 11")
	}
	e.now = e.now.Add(11 * time.Hour) // hour 22: 11h after the last activity
	if u := lookup(t, e, sid, secret); !u.Unlocked {
		t.Fatal("activity at hour 11 did not extend the wrap")
	}
}

func TestIdleTwelveHoursLocks(t *testing.T) {
	e, sid, secret, uk := encryptedSession(t)
	defer uk.Zero()
	e.now = e.now.Add(12*time.Hour + time.Second)
	u := lookup(t, e, sid, secret)
	if u.Unlocked || u.Key != nil {
		t.Fatal("unlocked after 12h idle")
	}
	if e.wrapCount("session") != 0 {
		t.Fatal("expired wrap kept")
	}
	// The session itself is still alive.
	if _, err := e.srv.sessions.Lookup(t.Context(), sid, ""); err != nil {
		t.Fatal(err)
	}
}

func TestExtensionIsThrottledToOncePerMinute(t *testing.T) {
	e, sid, secret, uk := encryptedSession(t)
	defer uk.Zero()
	read := func() (time.Time, time.Time) {
		var exp, used time.Time
		if err := e.st.DB.QueryRow(`SELECT expires_at, last_used_at FROM user_key_wraps WHERE kind='session'`).Scan(&exp, &used); err != nil {
			t.Fatal(err)
		}
		return exp, used
	}
	_, u0 := read()
	e.now = e.now.Add(30 * time.Second)
	lookup(t, e, sid, secret)
	if _, u1 := read(); !u1.Equal(u0) {
		t.Fatal("touched within a minute")
	}
	e.now = e.now.Add(31 * time.Second)
	lookup(t, e, sid, secret)
	if exp, u2 := read(); !u2.After(u0) || !exp.After(u0.Add(11*time.Hour+59*time.Minute)) {
		t.Fatal("not extended after a minute")
	}
}

func TestExtensionNeverOutlivesSession(t *testing.T) {
	e, sid, secret, uk := encryptedSession(t)
	defer uk.Zero()
	e.now = e.now.Add(sessionTTL - time.Hour)
	// Keep the wrap alive across the long jump, then touch it.
	if _, err := e.st.DB.Exec(`UPDATE user_key_wraps SET expires_at = $1 WHERE kind='session'`, e.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	lookup(t, e, sid, secret)
	var wrapExp, sessExp time.Time
	e.st.DB.QueryRow(`SELECT expires_at FROM user_key_wraps WHERE kind='session'`).Scan(&wrapExp)
	e.st.DB.QueryRow(`SELECT expires_at FROM sessions WHERE id_hash = $1`, hashOf(sid)).Scan(&sessExp)
	if wrapExp.After(sessExp) {
		t.Fatalf("wrap %v outlives session %v", wrapExp, sessExp)
	}
}

func TestUnlockSecretFromOtherSessionFails(t *testing.T) {
	e, sid, secret, uk := encryptedSession(t)
	defer uk.Zero()
	uid := e.userBySub("g-alice").ID
	sid2, _, err := e.srv.sessions.Create(t.Context(), uid)
	if err != nil {
		t.Fatal(err)
	}
	// Session 2 has no wrap of its own: the first session's secret opens nothing.
	if u := lookup(t, e, sid2, secret); u.Unlocked {
		t.Fatal("secret from another session unlocked")
	}
	// Moving the wrap row to session 2 does not help: ref_id is bound in the AAD.
	if _, err := e.st.DB.Exec(`UPDATE user_key_wraps SET ref_id = $1 WHERE kind='session'`, hashOf(sid2)); err != nil {
		t.Fatal(err)
	}
	if u := lookup(t, e, sid2, secret); u.Unlocked {
		t.Fatal("re-pointed wrap unlocked")
	}
	_ = sid
}

func TestWrongUnlockSecretIsLocked(t *testing.T) {
	e, sid, secret, uk := encryptedSession(t)
	defer uk.Zero()
	if u := lookup(t, e, sid, "not-the-secret"); u.Unlocked {
		t.Fatal("wrong secret unlocked")
	}
	if u := lookup(t, e, sid, secret); !u.Unlocked {
		t.Fatal("right secret failed")
	}
	// Revoked wrap.
	if _, err := e.st.DB.Exec(`UPDATE user_key_wraps SET revoked_at = now() WHERE kind='session'`); err != nil {
		t.Fatal(err)
	}
	if u := lookup(t, e, sid, secret); u.Unlocked {
		t.Fatal("revoked wrap unlocked")
	}
	// Expired wrap.
	if _, err := e.st.DB.Exec(`UPDATE user_key_wraps SET revoked_at = NULL, expires_at = $1 WHERE kind='session'`, e.now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if u := lookup(t, e, sid, secret); u.Unlocked {
		t.Fatal("expired wrap unlocked")
	}
}

func TestStolenSessionCookieAloneCannotUnlock(t *testing.T) {
	e, sid, _, uk := encryptedSession(t)
	defer uk.Zero()
	if u := lookup(t, e, sid, ""); u.Unlocked || u.Key != nil {
		t.Fatal("session id alone unlocked")
	}
	e.dropCookie(unlockCookie)
	if r, _ := e.get("/memories"); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("/memories with session only: %d", r.StatusCode)
	}
}

func TestSessionWithoutUnlockCookieIsLocked(t *testing.T) {
	e, _, secret, uk := encryptedSession(t)
	defer uk.Zero()
	e.setCookie(unlockCookie, secret)
	if r, _ := e.get("/memories"); r.StatusCode != 200 {
		t.Fatalf("with cookie: %d", r.StatusCode)
	}
	e.dropCookie(unlockCookie)
	if r, _ := e.get("/memories"); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("without cookie: %d", r.StatusCode)
	}
}

func TestLockedRoutesRedirectToUnlock(t *testing.T) {
	e, _, _, uk := encryptedSession(t) // no cookie set in the jar: locked
	defer uk.Zero()
	for _, path := range []string{"/memories", "/memories/deleted", "/graph", "/vault", "/settings"} {
		r, _ := e.get(path)
		want := "/unlock?next=" + url.QueryEscape(path)
		if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != want {
			t.Fatalf("%s: %d %q, want 303 %q", path, r.StatusCode, r.Header.Get("Location"), want)
		}
	}
	for _, path := range []string{"/settings", "/memories/delete", "/graph/rename"} {
		r, _ := e.post(path, url.Values{"csrf": {e.csrfFromAccount()}})
		if r.StatusCode != http.StatusLocked {
			t.Fatalf("POST %s: %d, want 423", path, r.StatusCode)
		}
	}
	for _, path := range []string{"/account", "/unlock", "/enroll"} {
		if r, _ := e.get(path); r.StatusCode >= 500 || r.StatusCode == http.StatusLocked {
			t.Fatalf("%s must stay reachable: %d", path, r.StatusCode)
		}
	}
}

func TestDeleteSessionWrapsExceptKeepsOne(t *testing.T) {
	e, sid, _, uk := encryptedSession(t)
	defer uk.Zero()
	uid := e.userBySub("g-alice").ID
	sid2, _, _ := e.srv.sessions.Create(t.Context(), uid)
	if _, _, err := e.srv.sessions.Unlock(t.Context(), sid2, uk); err != nil {
		t.Fatal(err)
	}
	if e.wrapCount("session") != 2 {
		t.Fatal("setup")
	}
	if err := e.st.DeleteSessionWrapsExcept(t.Context(), uid, hashOf(sid)); err != nil {
		t.Fatal(err)
	}
	var ref string
	if err := e.st.DB.QueryRow(`SELECT ref_id FROM user_key_wraps WHERE kind='session'`).Scan(&ref); err != nil || ref != hashOf(sid) {
		t.Fatalf("kept %q err %v", ref, err)
	}
}

func hashOf(id string) string { return accounts.HashToken(id) }

// csrfFromAccount reads the session's CSRF token from the database: the
// content pages that normally carry it are locked here.
func (e *env) csrfFromAccount() string {
	e.t.Helper()
	var c string
	if err := e.st.DB.QueryRow(`SELECT csrf FROM sessions WHERE id_hash = $1`, hashOf(e.cookieVal(sessionCookie))).Scan(&c); err != nil {
		e.t.Fatal(err)
	}
	return c
}
