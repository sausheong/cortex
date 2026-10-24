package web

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/pgtest"
)

type fakeIdP struct {
	mu       sync.Mutex
	ids      map[string]Identity // by code
	state    string
	nonce    string
	badNonce bool
}

func (f *fakeIdP) AuthURL(state, nonce string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state, f.nonce = state, nonce
	return "https://idp.test/auth?state=" + url.QueryEscape(state)
}

func (f *fakeIdP) Exchange(ctx context.Context, code, nonce string) (Identity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.badNonce {
		return Identity{}, errors.New("nonce mismatch")
	}
	id, ok := f.ids[code]
	if !ok {
		return Identity{}, errors.New("bad code")
	}
	return id, nil
}

type env struct {
	t       *testing.T
	srv     *server
	ts      *httptest.Server
	c       *http.Client
	jar     *cookiejar.Jar
	root    *cortex.Cortex
	st      *accounts.Store
	idp     *fakeIdP
	now     time.Time
	enq     []string
	enqUser []string
	provErr error

	// plain keeps signIn from enrolling the user: the account stays in state
	// "none", as a fresh sign-up does. Every other test works with encrypted
	// accounts and an unlocked session (plaintext accounts no longer exist).
	plain bool
	keys  map[string]*keys.UK
}

// testMounts lets per-page test files register their routes in newEnv.
var testMounts []func(mux *http.ServeMux, d Deps)

func newEnv(t *testing.T, mods ...func(*Config)) *env {
	t.Helper()
	dsn := pgtest.DSN(t)
	root, err := cortex.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e := &env{t: t, root: root, now: time.Now().UTC(), idp: &fakeIdP{ids: map[string]Identity{}}, keys: map[string]*keys.UK{}}
	e.st = &accounts.Store{DB: db, Now: func() time.Time { return e.now }}
	cfg := Config{PublicURL: "https://cortex.test", BootstrapEmail: "owner@x.test", Identity: e.idp}
	for _, m := range mods {
		m(&cfg)
	}
	e.srv = newServer(cfg,
		root, e.st, Options{Now: func() time.Time { return e.now }, Mounts: testMounts,
			Enqueue: func(ctx context.Context, userID, kind string) (string, error) {
				e.enq = append(e.enq, kind)
				e.enqUser = append(e.enqUser, userID)
				return "j_stub", nil
			}})
	e.srv.testProvider = func(ctx context.Context, s accounts.Settings) error { return e.provErr }
	e.ts = httptest.NewUnstartedServer(e.srv.handler())
	e.ts.StartTLS()
	t.Cleanup(e.ts.Close)
	e.newClient()
	return e
}

func (e *env) newClient() {
	e.jar, _ = cookiejar.New(nil)
	e.c = &http.Client{
		Jar:           e.jar,
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (e *env) get(path string) (*http.Response, string) {
	e.t.Helper()
	resp, err := e.c.Get(e.ts.URL + path)
	if err != nil {
		e.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func (e *env) post(path string, form url.Values) (*http.Response, string) {
	e.t.Helper()
	resp, err := e.c.PostForm(e.ts.URL+path, form)
	if err != nil {
		e.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

// start performs /auth/google/start and returns the state sent to the IdP.
func (e *env) start(next string) string {
	e.t.Helper()
	p := "/auth/google/start"
	if next != "" {
		p += "?next=" + url.QueryEscape(next)
	}
	resp, _ := e.get(p)
	if resp.StatusCode != http.StatusFound {
		e.t.Fatalf("start: %d", resp.StatusCode)
	}
	return e.idp.state
}

func (e *env) callback(state, code string) (*http.Response, string) {
	return e.get("/auth/google/callback?state=" + url.QueryEscape(state) + "&code=" + code)
}

// signIn runs the whole sign-in for identity id.
func (e *env) signIn(id Identity, next string) *http.Response {
	e.t.Helper()
	code := "code-" + id.Sub
	e.idp.ids[code] = id
	resp, _ := e.callback(e.start(next), code)
	if !e.plain {
		e.enroll(id)
	}
	return resp
}

// enroll turns the signed-in user into an encrypted account with an unlocked
// session: see ensureEnc, then the session is unlocked with the key.
func (e *env) enroll(id Identity) {
	e.t.Helper()
	u, err := e.root.UserByGoogleSub(e.t.Context(), id.Sub)
	if err != nil {
		return // the sign-in did not create an account (a refused identity)
	}
	uk := e.ensureEnc(u.ID)
	if sid := e.cookieVal(sessionCookie); sid != "" {
		secret, _, err := e.srv.sessions.Unlock(e.t.Context(), sid, uk)
		if err != nil {
			e.t.Fatal(err)
		}
		e.setCookie(unlockCookie, secret)
	}
}

// ensureEnc makes the user an encrypted account (once): the state flips to
// encrypted, a user key is created and the owner entity is created sealed, as
// enrollment does. It returns the key.
func (e *env) ensureEnc(userID string) *keys.UK {
	e.t.Helper()
	if uk := e.keys[userID]; uk != nil {
		return uk
	}
	uk, err := keys.NewUK()
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(uk.Zero)
	e.keys[userID] = uk
	var name, email string
	if err := e.st.DB.QueryRow(`SELECT name, email FROM users WHERE id = $1`, userID).Scan(&name, &email); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.st.DB.Exec(`UPDATE users SET encryption_state = 'encrypted' WHERE id = $1`, userID); err != nil {
		e.t.Fatal(err)
	}
	if name == "" {
		name = email
	}
	cx := e.root.ForUser(userID).WithKey(uk, "encrypted")
	defer cx.Release()
	owner := &cortex.Entity{Type: "person", Name: name, Source: "owner"}
	if err := cx.PutEntity(e.t.Context(), owner); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.st.DB.Exec(`UPDATE entities SET is_owner = TRUE WHERE id = $1`, owner.ID); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.st.DB.Exec(`UPDATE users SET owner_entity_id = $1 WHERE id = $2`, owner.ID, userID); err != nil {
		e.t.Fatal(err)
	}
	return uk
}

// cx returns a handle for user id holding the user's key; a user without one
// (created directly rather than by signing in) is encrypted first.
func (e *env) cx(id string, opts ...cortex.Option) *cortex.Cortex {
	e.t.Helper()
	uk := e.ensureEnc(id)
	cx := e.root.ForUser(id, opts...).WithKey(uk, "encrypted")
	e.t.Cleanup(cx.Release)
	return cx
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func (e *env) csrf() string {
	e.t.Helper()
	_, body := e.get("/settings")
	m := csrfRe.FindStringSubmatch(body)
	if m == nil { // an account that has not enrolled is sent to enrollment
		_, body = e.get("/enroll")
		m = csrfRe.FindStringSubmatch(body)
	}
	if m == nil {
		e.t.Fatalf("no csrf in page: %.200s", body)
	}
	return m[1]
}

func (e *env) userBySub(sub string) *cortex.User {
	e.t.Helper()
	u, err := e.root.UserByGoogleSub(e.t.Context(), sub)
	if err != nil {
		e.t.Fatal(err)
	}
	return u
}

func alice() Identity {
	return Identity{Sub: "g-alice", Email: "alice@x.test", Name: "Alice", EmailVerified: true}
}

func settingsForm(csrf string, kv ...string) url.Values {
	v := url.Values{"csrf": {csrf}, "provider": {"openai-compatible"}, "base_url": {"https://api.example.com/v1"},
		"api_key": {"sk-secret-3f9a"}, "chat_model": {"m"}, "embed_model": {"e"}, "embed_dims": {"512"}}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return v
}

func (e *env) insertEmbedding(userID string) {
	e.t.Helper()
	if _, err := e.st.DB.Exec(`INSERT INTO embeddings (id, ref_id, ref_type, user_id, embedding_ct, dims, generation)
		VALUES ($1,'r','memory',$2,'\x00'::bytea,512,1)`, "emb_"+userID, userID); err != nil {
		e.t.Fatal(err)
	}
}

func TestFirstSigninCreatesUserAndRedirectsToSettings(t *testing.T) {
	e := newEnv(t)
	resp := e.signIn(alice(), "")
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/settings" {
		t.Fatalf("got %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	u := e.userBySub("g-alice")
	if u.Email != "alice@x.test" || !u.EmailVerified || u.ID == cortex.BootstrapUserID {
		t.Fatalf("%+v", u)
	}
	if r, _ := e.get("/settings"); r.StatusCode != 200 {
		t.Fatalf("settings: %d", r.StatusCode)
	}
	// Second sign-in is an existing user: lands on /.
	e.newClient()
	if resp := e.signIn(alice(), ""); resp.Header.Get("Location") != "/" {
		t.Fatalf("existing user landed on %q", resp.Header.Get("Location"))
	}
}

func TestBootstrapBindsOnVerifiedEmail(t *testing.T) {
	e := newEnv(t)
	e.signIn(Identity{Sub: "g-owner", Email: "owner@x.test", Name: "Owner", EmailVerified: true}, "")
	u := e.userBySub("g-owner")
	if u.ID != cortex.BootstrapUserID {
		t.Fatalf("expected bootstrap user, got %s", u.ID)
	}
}

func TestUnverifiedBootstrapEmailCreatesNewUser(t *testing.T) {
	e := newEnv(t)
	e.signIn(Identity{Sub: "g-fake", Email: "owner@x.test", Name: "Fake", EmailVerified: false}, "")
	u := e.userBySub("g-fake")
	if u.ID == cortex.BootstrapUserID || u.EmailVerified {
		t.Fatalf("unverified email bound bootstrap: %+v", u)
	}
	users, _ := e.root.ListUsers(t.Context())
	for _, x := range users {
		if x.ID == cortex.BootstrapUserID && x.GoogleSub != "" {
			t.Fatal("bootstrap user was bound")
		}
	}
}

func TestCallbackRejectsStateMismatch(t *testing.T) {
	e := newEnv(t)
	e.idp.ids["c"] = alice()
	e.start("")
	resp, _ := e.callback("wrong", "c")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("got %d", resp.StatusCode)
	}
	if _, err := e.root.UserByGoogleSub(t.Context(), "g-alice"); err == nil {
		t.Fatal("user created")
	}
}

func TestCallbackRejectsNonceMismatch(t *testing.T) {
	e := newEnv(t)
	e.idp.ids["c"] = alice()
	st := e.start("")
	e.idp.badNonce = true
	resp, _ := e.callback(st, "c")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("got %d", resp.StatusCode)
	}
	if _, err := e.root.UserByGoogleSub(t.Context(), "g-alice"); err == nil {
		t.Fatal("user created")
	}
}

func TestCallbackRejectsMissingLoginCookie(t *testing.T) {
	e := newEnv(t)
	e.idp.ids["c"] = alice()
	st := e.start("")
	e.newClient() // drops the login cookie
	resp, _ := e.callback(st, "c")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("got %d", resp.StatusCode)
	}
}

func TestLoginAttemptSingleUse(t *testing.T) {
	e := newEnv(t)
	e.idp.ids["c"] = alice()
	st := e.start("")
	if resp, _ := e.callback(st, "c"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("first: %d", resp.StatusCode)
	}
	// Replay with the same login cookie value.
	u, _ := url.Parse(e.ts.URL)
	var login string
	for _, c := range e.jar.Cookies(u) {
		if c.Name == loginCookie {
			login = c.Value
		}
	}
	if login != "" {
		t.Fatal("login cookie should be cleared after use")
	}
	var n int
	e.st.DB.QueryRow(`SELECT count(*) FROM login_attempts`).Scan(&n)
	if n != 0 {
		t.Fatalf("attempt not consumed: %d", n)
	}
	if resp, _ := e.callback(st, "c"); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("replay: %d", resp.StatusCode)
	}
}

func TestReturnToMustBeSameOrigin(t *testing.T) {
	for _, tc := range []struct{ next, want string }{
		{"https://evil.example", "/"},
		{"//evil.example", "/"},
		{`/\evil`, "/"},
		{"/connections", "/connections"},
	} {
		e := newEnv(t)
		// An existing user, so that the landing page is return_to.
		if _, err := e.root.CreateUser(t.Context(), "g-alice", "alice@x.test", "Alice", true); err != nil {
			t.Fatal(err)
		}
		resp := e.signIn(alice(), tc.next)
		if got := resp.Header.Get("Location"); got != tc.want {
			t.Errorf("next=%q landed on %q, want %q", tc.next, got, tc.want)
		}
	}
}

func TestSigninRotatesSessionID(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	u, _ := url.Parse(e.ts.URL)
	sess := func() string {
		for _, c := range e.jar.Cookies(u) {
			if c.Name == sessionCookie {
				return c.Value
			}
		}
		return ""
	}
	old := sess()
	if old == "" {
		t.Fatal("no session cookie")
	}
	e.signIn(alice(), "")
	if now := sess(); now == "" || now == old {
		t.Fatalf("session not rotated: %q -> %q", old, now)
	}
	if _, err := e.srv.sessions.Lookup(t.Context(), old, ""); err == nil {
		t.Fatal("old session still valid")
	}
	// A session cookie planted before sign-in is dead afterwards.
	e.newClient()
	e.jar.SetCookies(u, []*http.Cookie{{Name: sessionCookie, Value: "planted", Path: "/", Secure: true}})
	e.signIn(alice(), "")
	if sess() == "planted" {
		t.Fatal("planted session survived sign-in")
	}
}

func TestDeletedUserSessionRejected(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	if r, _ := e.get("/settings"); r.StatusCode != 200 {
		t.Fatalf("settings: %d", r.StatusCode)
	}
	if err := e.st.DeleteAccount(t.Context(), e.userBySub("g-alice").ID); err != nil {
		t.Fatal(err)
	}
	if r, _ := e.get("/settings"); r.StatusCode != http.StatusFound || !strings.HasPrefix(r.Header.Get("Location"), "/auth/google/start") {
		t.Fatalf("deleted user got %d %q", r.StatusCode, r.Header.Get("Location"))
	}
}

func TestSigninRestoresDeletedAccountWithNotice(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	id := e.userBySub("g-alice").ID
	if err := e.st.DeleteAccount(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	e.newClient()
	e.idp.ids["c"] = alice()
	resp, body := e.callback(e.start(""), "c")
	if resp.StatusCode != 200 || !strings.Contains(body, "your account was restored") && !strings.Contains(body, "Your account was restored") {
		t.Fatalf("%d %.300s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "Reconnect your assistants") {
		t.Fatalf("no reconnect notice: %s", body)
	}
	if e.userBySub("g-alice").DeletedAt != nil {
		t.Fatal("not restored")
	}
	e.enroll(alice()) // the new session starts locked
	if r, _ := e.get("/settings"); r.StatusCode != 200 {
		t.Fatalf("settings after restore: %d", r.StatusCode)
	}
}

func TestDeletedPastWindowCannotSignIn(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	id := e.userBySub("g-alice").ID
	e.st.DeleteAccount(t.Context(), id)
	e.now = e.now.Add(31 * 24 * time.Hour)
	e.newClient()
	e.idp.ids["c"] = alice()
	if resp, _ := e.callback(e.start(""), "c"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("got %d", resp.StatusCode)
	}
}

func TestPostWithoutCSRFRejected(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	if r, _ := e.post("/settings", settingsForm("")); r.StatusCode != http.StatusForbidden {
		t.Fatalf("empty csrf: %d", r.StatusCode)
	}
	if r, _ := e.post("/settings", settingsForm("wrong")); r.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong csrf: %d", r.StatusCode)
	}
	if r, _ := e.post("/logout", url.Values{}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("logout without csrf: %d", r.StatusCode)
	}
	if _, err := e.st.GetSettings(t.Context(), e.userBySub("g-alice").ID, e.keys[e.userBySub("g-alice").ID]); !errors.Is(err, accounts.ErrNoSettings) {
		t.Fatal("settings saved without csrf")
	}
}

func TestLogout(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	if r, _ := e.post("/logout", url.Values{"csrf": {e.csrf()}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout: %d", r.StatusCode)
	}
	if r, _ := e.get("/settings"); r.StatusCode != http.StatusFound {
		t.Fatalf("after logout: %d", r.StatusCode)
	}
}

func TestSecurityHeadersOnAllPages(t *testing.T) {
	e := newEnv(t)
	check := func(name string, r *http.Response) {
		t.Helper()
		csp := r.Header.Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'self'", "script-src 'self' 'nonce-", "img-src 'self' data:", "connect-src 'self'", "frame-ancestors 'none'", "base-uri 'none'", "form-action 'self' https://accounts.google.com"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: CSP missing %q: %s", name, want, csp)
			}
		}
		if strings.Contains(csp, "unsafe-inline") || r.Header.Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s: bad headers: %v", name, r.Header)
		}
	}
	r, _ := e.get("/")
	check("/", r)
	r, _ = e.get("/nope")
	if r.StatusCode != 404 {
		t.Fatalf("404 page: %d", r.StatusCode)
	}
	check("404", r)
	e.signIn(alice(), "")
	r, _ = e.get("/settings")
	check("/settings", r)
	r, _ = e.post("/settings", settingsForm("bad"))
	if r.StatusCode != 403 {
		t.Fatalf("403 page: %d", r.StatusCode)
	}
	check("403", r)
}

func TestPutSettingsPromotesWhenNothingToReembed(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	// A user with no data changing model and dims.
	a, _ := e.root.CreateUser(ctx, "ga", "a@x.test", "A", true)
	s := accounts.Settings{BaseURL: "https://api.example.com/v1", APIKey: "k1", ChatModel: "m", EmbedModel: "e", EmbedDims: 512}
	if need, err := e.st.PutSettings(ctx, a.ID, s, e.ensureEnc(a.ID)); err != nil || need {
		t.Fatalf("first: %v %v", need, err)
	}
	s.EmbedModel, s.EmbedDims = "e2", 768
	if need, err := e.st.PutSettings(ctx, a.ID, s, e.ensureEnc(a.ID)); err != nil || need {
		t.Fatalf("no data: need=%v err=%v", need, err)
	}
	if act, _ := e.st.GetActiveEmbed(ctx, a.ID, e.keys[a.ID]); act.EmbedModel != "e2" || act.EmbedDims != 768 {
		t.Fatalf("active %+v", act)
	}
	// A user with data changing only the embed key.
	b, _ := e.root.CreateUser(ctx, "gb", "b@x.test", "B", true)
	s = accounts.Settings{BaseURL: "https://api.example.com/v1", APIKey: "k1", ChatModel: "m", EmbedModel: "e", EmbedDims: 512}
	e.st.PutSettings(ctx, b.ID, s, e.ensureEnc(b.ID))
	e.insertEmbedding(b.ID)
	s.EmbedAPIKey = "newkey-9999"
	if need, err := e.st.PutSettings(ctx, b.ID, s, e.ensureEnc(b.ID)); err != nil || need {
		t.Fatalf("key only: need=%v err=%v", need, err)
	}
	if act, _ := e.st.GetActiveEmbed(ctx, b.ID, e.keys[b.ID]); act.EmbedAPIKey != "newkey-9999" {
		t.Fatalf("key not swapped: %+v", act)
	}
	// A user with data changing the model keeps the old active embedder.
	s.EmbedModel = "e3"
	need, err := e.st.PutSettings(ctx, b.ID, s, e.ensureEnc(b.ID))
	if err != nil || !need {
		t.Fatalf("model change: need=%v err=%v", need, err)
	}
	if act, _ := e.st.GetActiveEmbed(ctx, b.ID, e.keys[b.ID]); act.EmbedModel != "e" {
		t.Fatalf("active changed early: %+v", act)
	}
}

func TestSettingsProviderFailureSavesNothing(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	e.provErr = errors.New("provider rejected the key")
	r, body := e.post("/settings", settingsForm(e.csrf()))
	if r.StatusCode != 400 || !strings.Contains(body, "provider rejected the key") {
		t.Fatalf("%d %.300s", r.StatusCode, body)
	}
	if _, err := e.st.GetSettings(t.Context(), e.userBySub("g-alice").ID, e.keys[e.userBySub("g-alice").ID]); !errors.Is(err, accounts.ErrNoSettings) {
		t.Fatal("settings saved after failed test")
	}
	if len(e.enq) != 0 {
		t.Fatalf("enqueued %v", e.enq)
	}
}

func TestSettingsRejectsPrivateURL(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	for _, u := range []string{"https://127.0.0.1/v1", "https://169.254.169.254/", "http://api.example.com/v1"} {
		r, _ := e.post("/settings", settingsForm(e.csrf(), "base_url", u))
		if r.StatusCode != 400 {
			t.Errorf("%s: %d", u, r.StatusCode)
		}
	}
	if _, err := e.st.GetSettings(t.Context(), e.userBySub("g-alice").ID, e.keys[e.userBySub("g-alice").ID]); !errors.Is(err, accounts.ErrNoSettings) {
		t.Fatal("settings saved")
	}
}

func TestSettingsProviderTestRateLimited(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	c := e.csrf()
	e.provErr = errors.New("provider unreachable")
	for i := 0; i < 10; i++ {
		if r, _ := e.post("/settings", settingsForm(c)); r.StatusCode != 400 {
			t.Fatalf("attempt %d: %d", i+1, r.StatusCode)
		}
	}
	if r, _ := e.post("/settings", settingsForm(c)); r.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("11th: %d", r.StatusCode)
	}
	e.now = e.now.Add(time.Minute)
	if r, _ := e.post("/settings", settingsForm(c)); r.StatusCode != 400 {
		t.Fatalf("after refill: %d", r.StatusCode)
	}
}

func TestSettingsEmbedChangeEnqueuesReembed(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	id := e.userBySub("g-alice").ID
	if r, _ := e.post("/settings", settingsForm(e.csrf())); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("first save: %d", r.StatusCode)
	}
	e.insertEmbedding(id)
	e.enq = nil
	// Chat-model-only change enqueues nothing.
	if r, _ := e.post("/settings", settingsForm(e.csrf(), "chat_model", "m2")); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("chat change: %d", r.StatusCode)
	}
	if len(e.enq) != 0 {
		t.Fatalf("chat-only change enqueued %v", e.enq)
	}
	// An embedding model change enqueues exactly one reembed.
	if r, _ := e.post("/settings", settingsForm(e.csrf(), "embed_model", "e-new")); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("embed change: %d", r.StatusCode)
	}
	if len(e.enq) != 1 || e.enq[0] != "reembed" || e.enqUser[0] != id {
		t.Fatalf("enqueued %v for %v", e.enq, e.enqUser)
	}
}

func TestNilEnqueueFailsReembedChange(t *testing.T) {
	e := newEnv(t)
	e.srv.opts.Enqueue = nil
	e.signIn(alice(), "")
	id := e.userBySub("g-alice").ID
	e.post("/settings", settingsForm(e.csrf()))
	e.insertEmbedding(id)
	r, body := e.post("/settings", settingsForm(e.csrf(), "embed_model", "e-new"))
	if r.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "re-embedding is not available") {
		t.Fatalf("%d %.300s", r.StatusCode, body)
	}
}

func (e *env) insertJob(id, userID, kind, status, errText string) {
	e.t.Helper()
	if _, err := e.st.DB.Exec(`INSERT INTO jobs (id, user_id, kind, status, error) VALUES ($1,$2,$3,$4,$5)`,
		id, userID, kind, status, errText); err != nil {
		e.t.Fatal(err)
	}
}

func TestFailedReembedShowsRetry(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	id := e.userBySub("g-alice").ID
	e.post("/settings", settingsForm(e.csrf()))
	e.insertEmbedding(id)
	e.post("/settings", settingsForm(e.csrf(), "embed_model", "e-new")) // pending
	e.insertJob("j_failed", id, "reembed", "failed", "provider unreachable")
	e.enq = nil
	_, body := e.get("/settings")
	if !strings.Contains(body, "provider unreachable") || !strings.Contains(body, "Update memories now") {
		t.Fatalf("no error/retry: %s", body)
	}
	if r, _ := e.post("/settings/reembed", url.Values{"csrf": {e.csrf()}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("retry: %d", r.StatusCode)
	}
	if len(e.enq) != 1 || e.enq[0] != "reembed" {
		t.Fatalf("retry enqueued %v", e.enq)
	}
	// A queued/running job blocks another retry.
	e.insertJob("j_run", id, "reembed", "running", "")
	e.post("/settings/reembed", url.Values{"csrf": {e.csrf()}})
	if len(e.enq) != 1 {
		t.Fatalf("retry while running enqueued %v", e.enq)
	}
	// Retry without CSRF is refused.
	if r, _ := e.post("/settings/reembed", url.Values{}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("retry without csrf: %d", r.StatusCode)
	}
}

func TestSettingsShowsMaskedKey(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	e.post("/settings", settingsForm(e.csrf()))
	_, body := e.get("/settings")
	if !strings.Contains(body, "••••3f9a") || strings.Contains(body, "sk-secret") {
		t.Fatalf("key not masked: %s", body)
	}
	// A blank key on save keeps the stored one.
	e.post("/settings", settingsForm(e.csrf(), "api_key", "", "chat_model", "m9"))
	s, err := e.st.GetSettings(t.Context(), e.userBySub("g-alice").ID, e.keys[e.userBySub("g-alice").ID])
	if err != nil || s.APIKey != "sk-secret-3f9a" || s.ChatModel != "m9" {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestSigninPageHasOperatorNotice(t *testing.T) {
	e := newEnv(t)
	_, body := e.get("/")
	if !strings.Contains(body, "The operator of this service can technically access data stored here.") {
		t.Fatalf("no notice: %s", body)
	}
}

func TestJobProgressOwnOnly(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	aid := e.userBySub("g-alice").ID
	b, _ := e.root.CreateUser(t.Context(), "g-bob", "bob@x.test", "Bob", true)
	e.insertJob("j_a", aid, "reembed", "running", "")
	e.insertJob("j_b", b.ID, "reembed", "running", "")
	r1, b1 := e.get("/settings/job/j_b")
	r2, b2 := e.get("/settings/job/j_absent")
	if r1.StatusCode != 404 || r2.StatusCode != 404 || b1 != b2 {
		t.Fatalf("foreign %d %q vs absent %d %q", r1.StatusCode, b1, r2.StatusCode, b2)
	}
	if r, body := e.get("/settings/job/j_a"); r.StatusCode != 200 || !strings.Contains(body, `"running"`) {
		t.Fatalf("own: %d %s", r.StatusCode, body)
	}
}

func TestSettingsShowsEmbeddingsSection(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	_, body := e.get("/settings")
	if strings.Contains(body, "<details") || strings.Contains(body, "Advanced") {
		t.Fatal("embeddings still collapsed")
	}
	for _, want := range []string{"<legend>Embeddings</legend>", `name="embed_model"`, `name="embed_dims"`, `name="embed_base_url"`, `name="embed_api_key"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s", want)
		}
	}
	// A rejected embed URL still shows its inline error.
	r, body := e.post("/settings", settingsForm(e.csrf(), "embed_base_url", "http://embed.example.com/v1"))
	if r.StatusCode != 400 || !strings.Contains(body, `name="embed_base_url"`) {
		t.Fatalf("embed url rejection: %d", r.StatusCode)
	}
}

func TestSettingsFieldErrorIsInline(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	r, body := e.post("/settings", settingsForm(e.csrf(), "base_url", "http://api.example.com/v1"))
	if r.StatusCode != 400 || !strings.Contains(body, `name="base_url"`) ||
		!strings.Contains(body, `aria-describedby="err-base h-base" aria-invalid="true"`) ||
		!strings.Contains(body, `id="err-base" role="alert">base URL must use https`) ||
		!strings.Contains(body, `<strong>Not saved.</strong>`) {
		t.Fatalf("inline field error missing: %d", r.StatusCode)
	}
	if strings.Contains(body, `name="embed_base_url"`+" ") && strings.Contains(body, `id="err-ebase" role="alert">base`) {
		t.Fatal("error leaked to the embedding field")
	}
}

var scriptTag = regexp.MustCompile(`<script\b[^>]*>`)

// Each page's CSP carries a fresh nonce and every inline script uses it.
func TestCSPNonceMatchesInlineScripts(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	seen := map[string]bool{}
	for _, p := range []string{"/", "/settings", "/memories", "/connections", "/vault", "/graph", "/account"} {
		r, body := e.get(p)
		if r.StatusCode != 200 {
			t.Errorf("%s: %d", p, r.StatusCode)
			continue
		}
		m := regexp.MustCompile(`'nonce-([^']+)'`).FindStringSubmatch(r.Header.Get("Content-Security-Policy"))
		if m == nil || len(m[1]) < 16 {
			t.Errorf("%s: no nonce in CSP", p)
			continue
		}
		if seen[m[1]] {
			t.Errorf("%s: nonce reused", p)
		}
		seen[m[1]] = true
		for _, tag := range scriptTag.FindAllString(body, -1) {
			if strings.Contains(tag, " src=") {
				continue
			}
			if !strings.Contains(tag, `nonce="`+m[1]+`"`) {
				t.Errorf("%s: inline script without this response's nonce: %s", p, tag)
			}
		}
	}
}

// legacyToken stores an agent token the way pre-encryption versions did: a
// connection and a token hash with no key wrap.
func (e *env) legacyToken(userID, name string) (string, *accounts.Connection) {
	e.t.Helper()
	conn, err := e.st.CreateConnection(e.t.Context(), userID, "agent", name, "")
	if err != nil {
		e.t.Fatal(err)
	}
	tok := "ctx_legacy_" + conn.ID
	if _, err := e.st.DB.Exec(`INSERT INTO agent_tokens (id, connection_id, token_hash) VALUES ($1,$2,$3)`,
		"t_"+conn.ID, conn.ID, accounts.HashToken(tok)); err != nil {
		e.t.Fatal(err)
	}
	return tok, conn
}
