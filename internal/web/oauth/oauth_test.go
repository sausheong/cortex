package oauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/pgtest"
	"github.com/sausheong/cortex/internal/web"
)

type stubIdP struct{}

func (stubIdP) AuthURL(state, nonce string) string { return "https://idp.test/" }
func (stubIdP) Exchange(ctx context.Context, code, nonce string) (web.Identity, error) {
	return web.Identity{}, fmt.Errorf("unused")
}

type sess struct{ id, csrf, userID, unlock string }

type env struct {
	t    *testing.T
	root *cortex.Cortex
	st   *accounts.Store
	h    http.Handler
	ts   *httptest.Server
	hc   *http.Client
	now  time.Time
	logs *bytes.Buffer
}

func newEnv(t *testing.T, o Options) *env {
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
	e := &env{t: t, root: root, now: time.Now().UTC(), logs: &bytes.Buffer{}}
	e.st = &accounts.Store{DB: db, Now: func() time.Time { return e.now }}
	mcp := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if _, _, _, err := e.st.ResolveBearer(r.Context(), tok); err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	logger := slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e.h = web.NewServer(web.Config{PublicURL: "https://cortex.test", BootstrapEmail: "owner@x.test", Identity: stubIdP{}, Logger: logger},
		root, e.st, web.Options{Now: func() time.Time { return e.now }, MCP: mcp, Mounts: []func(*http.ServeMux, web.Deps){Mount(o)}})
	e.ts = httptest.NewTLSServer(e.h)
	t.Cleanup(e.ts.Close)
	e.hc = &http.Client{
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return e
}

func (e *env) user(sub string) sess {
	e.t.Helper()
	u, err := e.root.CreateUser(e.t.Context(), sub, sub+"@x.test", sub, true)
	if err != nil {
		e.t.Fatal(err)
	}
	sid, csrf, err := web.NewSessions(e.st, func() time.Time { return e.now }).Create(e.t.Context(), u.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return sess{id: sid, csrf: csrf, userID: u.ID}
}

func (e *env) do(method, path string, s *sess, body io.Reader, ctype string) (*http.Response, string) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.ts.URL+path, body)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if s != nil {
		req.AddCookie(&http.Cookie{Name: "cortex_session", Value: s.id})
		if s.unlock != "" {
			req.AddCookie(&http.Cookie{Name: "cortex_unlock", Value: s.unlock})
		}
	}
	resp, err := e.hc.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func (e *env) get(path string, s *sess) (*http.Response, string) {
	return e.do("GET", path, s, nil, "")
}

func (e *env) postForm(path string, s *sess, v url.Values) (*http.Response, string) {
	if s != nil && v.Get("csrf") == "" {
		v.Set("csrf", s.csrf)
	}
	return e.do("POST", path, s, strings.NewReader(v.Encode()), "application/x-www-form-urlencoded")
}

func (e *env) register(name string, uris ...string) string {
	e.t.Helper()
	b, _ := json.Marshal(map[string]any{"client_name": name, "redirect_uris": uris})
	resp, body := e.do("POST", "/oauth/register", nil, bytes.NewReader(b), "application/json")
	if resp.StatusCode != 201 {
		e.t.Fatalf("register: %d %s", resp.StatusCode, body)
	}
	var out struct {
		ClientID string `json:"client_id"`
	}
	_ = json.Unmarshal([]byte(body), &out)
	return out.ClientID
}

const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"

func challenge(v string) string {
	s := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(s[:])
}

func authQuery(clientID, redirect string, kv ...string) string {
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect},
		"code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}, "state": {"st1"}, "scope": {"cortex offline_access"}}
	for i := 0; i+1 < len(kv); i += 2 {
		q.Set(kv[i], kv[i+1])
	}
	return "/oauth/authorize?" + q.Encode()
}

var ridRe = regexp.MustCompile(`name="request_id" value="([^"]+)"`)

func (e *env) consent(s *sess, clientID, redirect string) (rid, body string) {
	e.t.Helper()
	resp, body := e.get(authQuery(clientID, redirect), s)
	if resp.StatusCode != 200 {
		e.t.Fatalf("consent: %d %s", resp.StatusCode, body)
	}
	m := ridRe.FindStringSubmatch(body)
	if m == nil {
		e.t.Fatalf("no request id: %s", body)
	}
	return m[1], body
}

func (e *env) allow(s *sess, rid string) *http.Response {
	resp, _ := e.postForm("/oauth/authorize", s, url.Values{"request_id": {rid}, "decision": {"allow"}})
	return resp
}

func codeFrom(t *testing.T, resp *http.Response) string {
	t.Helper()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || loc.Query().Get("code") == "" {
		t.Fatalf("no code: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	return loc.Query().Get("code")
}

type tokens struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
	Err     string `json:"error"`
}

func (e *env) exchange(code, clientID, redirect string) (*http.Response, tokens) {
	resp, body := e.postForm("/oauth/token", nil, url.Values{"grant_type": {"authorization_code"}, "code": {code},
		"client_id": {clientID}, "redirect_uri": {redirect}, "code_verifier": {verifier}})
	var t tokens
	_ = json.Unmarshal([]byte(body), &t)
	return resp, t
}

func (e *env) refresh(rt, clientID string) (*http.Response, tokens) {
	resp, body := e.postForm("/oauth/token", nil, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}, "client_id": {clientID}})
	var t tokens
	_ = json.Unmarshal([]byte(body), &t)
	return resp, t
}

func (e *env) mcp(tok string) int {
	req, _ := http.NewRequest("POST", e.ts.URL+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := e.hc.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

const cb = "https://app.example/cb"

// connect runs the whole flow and returns the tokens.
func (e *env) connect(s *sess, clientID string) tokens {
	e.t.Helper()
	rid, _ := e.consent(s, clientID, cb)
	resp, tk := e.exchange(codeFrom(e.t, e.allow(s, rid)), clientID, cb)
	if resp.StatusCode != 200 {
		e.t.Fatalf("exchange: %d %+v", resp.StatusCode, tk)
	}
	return tk
}

func TestMetadataDocuments(t *testing.T) {
	e := newEnv(t, Options{})
	_, body := e.get("/.well-known/oauth-protected-resource", nil)
	var pr struct {
		Resource string   `json:"resource"`
		AS       []string `json:"authorization_servers"`
		Scopes   []string `json:"scopes_supported"`
	}
	_ = json.Unmarshal([]byte(body), &pr)
	if pr.Resource != "https://cortex.test/mcp" || len(pr.AS) != 1 || pr.AS[0] != "https://cortex.test" || len(pr.Scopes) != 2 {
		t.Fatalf("%s", body)
	}
	_, body = e.get("/.well-known/oauth-authorization-server", nil)
	var as map[string]any
	_ = json.Unmarshal([]byte(body), &as)
	if as["issuer"] != "https://cortex.test" || as["registration_endpoint"] != "https://cortex.test/oauth/register" ||
		as["client_id_metadata_document_supported"] != true || fmt.Sprint(as["code_challenge_methods_supported"]) != "[S256]" {
		t.Fatalf("%s", body)
	}
}

func TestAuthorizeRequiresPKCES256(t *testing.T) {
	e := newEnv(t, Options{})
	s := e.user("a")
	id := e.register("App", cb)
	resp, _ := e.get(authQuery(id, cb, "code_challenge_method", "plain"), &s)
	if resp.StatusCode != 303 || !strings.Contains(resp.Header.Get("Location"), "error=invalid_request") {
		t.Fatalf("plain: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, _ = e.get(authQuery(id, cb, "code_challenge", ""), &s)
	if !strings.Contains(resp.Header.Get("Location"), "error=invalid_request") {
		t.Fatalf("empty challenge: %q", resp.Header.Get("Location"))
	}
	resp, _ = e.get(authQuery(id, cb, "scope", "cortex admin"), &s)
	if !strings.Contains(resp.Header.Get("Location"), "error=invalid_scope") {
		t.Fatalf("scope: %q", resp.Header.Get("Location"))
	}
}

func TestAuthorizeRequiresSession(t *testing.T) {
	e := newEnv(t, Options{})
	id := e.register("App", cb)
	resp, _ := e.get(authQuery(id, cb), nil)
	if resp.StatusCode != 302 || !strings.HasPrefix(resp.Header.Get("Location"), "/auth/google/start?next=%2Foauth%2Fauthorize") {
		t.Fatalf("%d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestRedirectURIExactMatch(t *testing.T) {
	e := newEnv(t, Options{})
	s := e.user("a")
	id := e.register("App", cb)
	for _, bad := range []string{cb + "/", cb + "?x=1", "https://app.example/cb2", "https://evil.example/cb", "https://APP.example/cb"} {
		resp, _ := e.get(authQuery(id, bad), &s)
		if resp.StatusCode != 400 {
			t.Fatalf("%q accepted: %d", bad, resp.StatusCode)
		}
	}
	if resp, _ := e.get(authQuery(id, cb), &s); resp.StatusCode != 200 {
		t.Fatalf("exact: %d", resp.StatusCode)
	}
}

func TestRegisterRejectsBadRedirectSchemes(t *testing.T) {
	e := newEnv(t, Options{})
	post := func(uri string) int {
		b, _ := json.Marshal(map[string]any{"client_name": "x", "redirect_uris": []string{uri}})
		resp, _ := e.do("POST", "/oauth/register", nil, bytes.NewReader(b), "application/json")
		return resp.StatusCode
	}
	for _, bad := range []string{"javascript:alert(1)", "myapp://cb", "http://evil.example/cb", "https://u:p@x.example/cb", "https://x.example/cb#f"} {
		if c := post(bad); c != 400 {
			t.Errorf("%q: %d", bad, c)
		}
	}
	for _, ok := range []string{"https://x.example/cb", "http://localhost:8123/cb", "http://127.0.0.1/cb", "http://[::1]:9/cb"} {
		if c := post(ok); c != 201 {
			t.Errorf("%q: %d", ok, c)
		}
	}
}

func TestRegisterBodyCapped(t *testing.T) {
	e := newEnv(t, Options{})
	body := `{"client_name":"` + strings.Repeat("a", 65<<10) + `","redirect_uris":["https://x.example/cb"]}`
	resp, _ := e.do("POST", "/oauth/register", nil, strings.NewReader(body), "application/json")
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("%d", resp.StatusCode)
	}
}

func cimdServer(t *testing.T, doc func(url string) string) (*httptest.Server, *http.Client) {
	t.Helper()
	var ts *httptest.Server
	ts = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, doc(ts.URL+r.URL.Path))
	}))
	t.Cleanup(ts.Close)
	return ts, &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func TestConsentShowsHostsAndUnverified(t *testing.T) {
	ts, hc := cimdServer(t, func(u string) string {
		return fmt.Sprintf(`{"client_id":%q,"client_name":"Claude","redirect_uris":["https://claude.example/cb"]}`, u)
	})
	e := newEnv(t, Options{CIMDClient: hc})
	s := e.user("a")
	dcr := e.register("ChatGPT", "https://evil.example/cb")
	_, body := e.consent(&s, dcr, "https://evil.example/cb")
	if !strings.Contains(body, "evil.example") || !strings.Contains(body, "unverified") || !strings.Contains(body, "ChatGPT") {
		t.Fatalf("dcr consent: %s", body)
	}
	cid := ts.URL + "/client.json"
	_, body = e.consent(&s, cid, "https://claude.example/cb")
	host := strings.TrimPrefix(ts.URL, "https://")
	if !strings.Contains(body, host) || strings.Contains(body, "unverified") || !strings.Contains(body, "claude.example") {
		t.Fatalf("cimd consent: %s", body)
	}
}

func TestConsentAlwaysShownOnReauthorize(t *testing.T) {
	e := newEnv(t, Options{})
	s := e.user("a")
	id := e.register("App", cb)
	e.connect(&s, id)
	resp, body := e.get(authQuery(id, cb), &s)
	if resp.StatusCode != 200 || !strings.Contains(body, `name="request_id"`) {
		t.Fatalf("expected consent, got %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestConsentPostUsesStoredRequest(t *testing.T) {
	e := newEnv(t, Options{})
	a, b := e.user("a"), e.user("b")
	id := e.register("App", cb)
	other := e.register("Other", "https://other.example/cb")
	rid, _ := e.consent(&a, id, cb)
	resp, _ := e.postForm("/oauth/authorize", &a, url.Values{"request_id": {rid}, "decision": {"allow"},
		"redirect_uri": {"https://evil.example/cb"}, "client_id": {other}, "code_challenge": {"x"}})
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, cb+"?") || !strings.Contains(loc, "code=") || !strings.Contains(loc, "state=st1") {
		t.Fatalf("tampered fields used: %q", loc)
	}
	// Replay.
	if resp, _ := e.postForm("/oauth/authorize", &a, url.Values{"request_id": {rid}, "decision": {"allow"}}); resp.StatusCode != 404 {
		t.Fatalf("replay: %d", resp.StatusCode)
	}
	// Foreign request ID.
	rid2, _ := e.consent(&a, id, cb)
	if resp, _ := e.postForm("/oauth/authorize", &b, url.Values{"request_id": {rid2}, "decision": {"allow"}}); resp.StatusCode != 404 {
		t.Fatalf("foreign: %d", resp.StatusCode)
	}
	if resp, _ := e.postForm("/oauth/authorize", &a, url.Values{"request_id": {"ar_nope"}, "decision": {"allow"}}); resp.StatusCode != 404 {
		t.Fatalf("absent: %d", resp.StatusCode)
	}
	// Expired.
	rid3, _ := e.consent(&a, id, cb)
	e.now = e.now.Add(11 * time.Minute)
	a2 := e.refreshSession(a)
	if resp, _ := e.postForm("/oauth/authorize", &a2, url.Values{"request_id": {rid3}, "decision": {"allow"}}); resp.StatusCode != 404 {
		t.Fatalf("expired: %d", resp.StatusCode)
	}
	// Deny redirects with access_denied and no code.
	rid4, _ := e.consent(&a2, id, cb)
	resp, _ = e.postForm("/oauth/authorize", &a2, url.Values{"request_id": {rid4}, "decision": {"deny"}})
	if l := resp.Header.Get("Location"); !strings.Contains(l, "error=access_denied") || strings.Contains(l, "code=") {
		t.Fatalf("deny: %q", l)
	}
}

// refreshSession issues a new session after the injected clock moved on.
func (e *env) refreshSession(s sess) sess {
	sid, csrf, err := web.NewSessions(e.st, func() time.Time { return e.now }).Create(e.t.Context(), s.userID)
	if err != nil {
		e.t.Fatal(err)
	}
	return sess{id: sid, csrf: csrf, userID: s.userID}
}

func TestCodeExchangeAndMCPCall(t *testing.T) {
	e := newEnv(t, Options{})
	s := e.user("a")
	id := e.register("App", cb)
	tk := e.connect(&s, id)
	if tk.Access == "" || tk.Refresh == "" {
		t.Fatalf("%+v", tk)
	}
	if c := e.mcp(tk.Access); c != 200 {
		t.Fatalf("mcp: %d", c)
	}
	if c := e.mcp("bogus"); c != 401 {
		t.Fatalf("bogus: %d", c)
	}
}

func TestExchangeValidatesPKCEAndBindings(t *testing.T) {
	e := newEnv(t, Options{})
	s := e.user("a")
	id := e.register("App", cb)
	mk := func() string { rid, _ := e.consent(&s, id, cb); return codeFrom(t, e.allow(&s, rid)) }
	resp, body := e.postForm("/oauth/token", nil, url.Values{"grant_type": {"authorization_code"}, "code": {mk()},
		"client_id": {id}, "redirect_uri": {cb}, "code_verifier": {strings.Repeat("a", 43)}})
	if resp.StatusCode != 400 || !strings.Contains(body, "invalid_grant") {
		t.Fatalf("bad verifier: %d %s", resp.StatusCode, body)
	}
	if resp, _ := e.exchange(mk(), id, "https://app.example/other"); resp.StatusCode != 400 {
		t.Fatalf("bad redirect: %d", resp.StatusCode)
	}
}

func TestCodeSingleUse(t *testing.T) {
	e := newEnv(t, Options{})
	s := e.user("a")
	id := e.register("App", cb)
	rid, _ := e.consent(&s, id, cb)
	code := codeFrom(t, e.allow(&s, rid))
	_, tk := e.exchange(code, id, cb)
	if resp, _ := e.exchange(code, id, cb); resp.StatusCode != 400 {
		t.Fatalf("replay: %d", resp.StatusCode)
	}
	if c := e.mcp(tk.Access); c != 401 {
		t.Fatalf("code replay should revoke issued tokens, got %d", c)
	}
}

func TestCodeExpiresAfter60s(t *testing.T) {
	e := newEnv(t, Options{})
	s := e.user("a")
	id := e.register("App", cb)
	rid, _ := e.consent(&s, id, cb)
	code := codeFrom(t, e.allow(&s, rid))
	e.now = e.now.Add(61 * time.Second)
	if resp, _ := e.exchange(code, id, cb); resp.StatusCode != 400 {
		t.Fatalf("%d", resp.StatusCode)
	}
}

func TestAccessTokenExpiresAfter1h(t *testing.T) {
	e := newEnv(t, Options{})
	s := e.user("a")
	id := e.register("App", cb)
	tk := e.connect(&s, id)
	e.now = e.now.Add(59 * time.Minute)
	if c := e.mcp(tk.Access); c != 200 {
		t.Fatalf("at 59m: %d", c)
	}
	e.now = e.now.Add(2 * time.Minute)
	if c := e.mcp(tk.Access); c != 401 {
		t.Fatalf("at 61m: %d", c)
	}
}

func TestRefreshExpiresAfter90DaysIdle(t *testing.T) {
	e := newEnv(t, Options{})
	s := e.user("a")
	id := e.register("App", cb)
	tk := e.connect(&s, id)
	day := 24 * time.Hour
	e.now = e.now.Add(89 * day)
	resp, t2 := e.refresh(tk.Refresh, id)
	if resp.StatusCode != 200 || t2.Refresh == "" || t2.Refresh == tk.Refresh {
		t.Fatalf("day 89: %d %+v", resp.StatusCode, t2)
	}
	e.now = e.now.Add(89 * day) // 89 idle days after the rotation: extended
	resp, t3 := e.refresh(t2.Refresh, id)
	if resp.StatusCode != 200 {
		t.Fatalf("rotation did not extend: %d", resp.StatusCode)
	}
	e.now = e.now.Add(91 * day)
	if resp, _ := e.refresh(t3.Refresh, id); resp.StatusCode != 400 {
		t.Fatalf("day 91 idle: %d", resp.StatusCode)
	}
}

func TestRefreshRotationAndReuseRevokes(t *testing.T) {
	e := newEnv(t, Options{})
	s := e.user("a")
	id := e.register("App", cb)
	tk := e.connect(&s, id)
	_, t2 := e.refresh(tk.Refresh, id)
	if c := e.mcp(t2.Access); c != 200 {
		t.Fatalf("rotated access: %d", c)
	}
	// Reusing the old refresh token kills the whole grant.
	if resp, _ := e.refresh(tk.Refresh, id); resp.StatusCode != 400 {
		t.Fatalf("reuse: %d", resp.StatusCode)
	}
	if resp, _ := e.refresh(t2.Refresh, id); resp.StatusCode != 400 {
		t.Fatalf("grant should be revoked: %d", resp.StatusCode)
	}
	if c := e.mcp(t2.Access); c != 401 {
		t.Fatalf("access after reuse: %d", c)
	}
	// A different client cannot use the token.
	other := e.register("Other", cb)
	tk2 := e.connect(&s, id)
	if resp, _ := e.refresh(tk2.Refresh, other); resp.StatusCode != 400 {
		t.Fatalf("cross-client: %d", resp.StatusCode)
	}
}

func TestRefreshFailsForDeletedUser(t *testing.T) {
	e := newEnv(t, Options{})
	s := e.user("a")
	id := e.register("App", cb)
	tk := e.connect(&s, id)
	if err := e.st.DeleteAccount(t.Context(), s.userID); err != nil {
		t.Fatal(err)
	}
	if resp, _ := e.refresh(tk.Refresh, id); resp.StatusCode != 400 {
		t.Fatalf("%d", resp.StatusCode)
	}
	if c := e.mcp(tk.Access); c != 401 {
		t.Fatalf("access: %d", c)
	}
}

func TestAuthorizeFailsForDeletedUser(t *testing.T) {
	e := newEnv(t, Options{})
	s := e.user("a")
	id := e.register("App", cb)
	rid, _ := e.consent(&s, id, cb)
	if err := e.st.DeleteAccount(t.Context(), s.userID); err != nil {
		t.Fatal(err)
	}
	if resp, _ := e.get(authQuery(id, cb), &s); resp.StatusCode != 302 || !strings.HasPrefix(resp.Header.Get("Location"), "/auth/google/start") {
		t.Fatalf("get: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ := e.postForm("/oauth/authorize", &s, url.Values{"request_id": {rid}, "decision": {"allow"}}); resp.StatusCode != 401 {
		t.Fatalf("post: %d", resp.StatusCode)
	}
}

func TestCIMDRejectsPrivateIP(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("private CIMD host was contacted")
	}))
	defer ts.Close()
	e := newEnv(t, Options{})
	e.st.AllowPrivate = true // must not lift the CIMD restriction
	s := e.user("a")
	resp, _ := e.get(authQuery(ts.URL+"/client.json", cb), &s)
	if resp.StatusCode != 400 {
		t.Fatalf("%d", resp.StatusCode)
	}
}

func TestCIMDCapsAndTimesOut(t *testing.T) {
	big, hc := cimdServer(t, func(u string) string {
		return fmt.Sprintf(`{"client_id":%q,"client_name":"%s","redirect_uris":["https://x.example/cb"]}`, u, strings.Repeat("a", 70<<10))
	})
	e := newEnv(t, Options{CIMDClient: hc, CIMDTimeout: 200 * time.Millisecond})
	s := e.user("a")
	if resp, _ := e.get(authQuery(big.URL+"/c.json", "https://x.example/cb"), &s); resp.StatusCode != 400 {
		t.Fatalf("oversize: %d", resp.StatusCode)
	}
	slow := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer slow.Close()
	start := time.Now()
	if resp, _ := e.get(authQuery(slow.URL+"/c.json", "https://x.example/cb"), &s); resp.StatusCode != 400 {
		t.Fatalf("slow: %d", resp.StatusCode)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("timeout not enforced")
	}
	// client_id mismatch and bad redirect scheme.
	bad, hc2 := cimdServer(t, func(u string) string {
		return `{"client_id":"https://other.example/c.json","client_name":"x","redirect_uris":["https://x.example/cb"]}`
	})
	e2 := newEnv(t, Options{CIMDClient: hc2})
	s2 := e2.user("a")
	if resp, _ := e2.get(authQuery(bad.URL+"/c.json", "https://x.example/cb"), &s2); resp.StatusCode != 400 {
		t.Fatalf("mismatch: %d", resp.StatusCode)
	}
	badURI, hc3 := cimdServer(t, func(u string) string {
		return fmt.Sprintf(`{"client_id":%q,"client_name":"x","redirect_uris":["myapp://cb"]}`, u)
	})
	e3 := newEnv(t, Options{CIMDClient: hc3})
	s3 := e3.user("a")
	if resp, _ := e3.get(authQuery(badURI.URL+"/c.json", "myapp://cb"), &s3); resp.StatusCode != 400 {
		t.Fatalf("scheme: %d", resp.StatusCode)
	}
}

func TestReauthorizeReusesConnection(t *testing.T) {
	e := newEnv(t, Options{})
	s := e.user("a")
	id := e.register("App", cb)
	e.connect(&s, id)
	before, _ := e.st.ListConnections(t.Context(), s.userID)
	e.connect(&s, id)
	after, _ := e.st.ListConnections(t.Context(), s.userID)
	if len(before) != 1 || len(after) != 1 || before[0].ID != after[0].ID {
		t.Fatalf("%+v %+v", before, after)
	}
}

func TestReconnectAfterRevokeNewConnection(t *testing.T) {
	e := newEnv(t, Options{})
	s := e.user("a")
	id := e.register("App", cb)
	tk := e.connect(&s, id)
	conns, _ := e.st.ListConnections(t.Context(), s.userID)
	if err := e.st.RevokeConnection(t.Context(), s.userID, conns[0].ID); err != nil {
		t.Fatal(err)
	}
	if c := e.mcp(tk.Access); c != 401 {
		t.Fatalf("access after revoke: %d", c)
	}
	if resp, _ := e.refresh(tk.Refresh, id); resp.StatusCode != 400 {
		t.Fatalf("refresh after revoke: %d", resp.StatusCode)
	}
	tk2 := e.connect(&s, id)
	conns2, _ := e.st.ListConnections(t.Context(), s.userID)
	live := 0
	for _, c := range conns2 {
		if c.RevokedAt == nil {
			live++
			if c.ID == conns[0].ID {
				t.Fatalf("revoked connection reused: %+v", conns2)
			}
		}
	}
	if len(conns2) != 2 || live != 1 {
		t.Fatalf("%+v", conns2)
	}
	if e.mcp(tk2.Access) != 200 || e.mcp(tk.Access) != 401 {
		t.Fatal("old/new token states wrong")
	}
}

func TestExpiredUnauthorizedDCRClientRejected(t *testing.T) {
	e := newEnv(t, Options{})
	s := e.user("a")
	id := e.register("App", cb)
	e.now = e.now.Add(25 * time.Hour)
	s = e.refreshSession(s)
	if resp, _ := e.get(authQuery(id, cb), &s); resp.StatusCode != 400 {
		t.Fatalf("authorize: %d", resp.StatusCode)
	}
	if resp, _ := e.exchange("oc_x", id, cb); resp.StatusCode != 401 {
		t.Fatalf("token: %d", resp.StatusCode)
	}
	// An authorized client does not expire.
	e2 := newEnv(t, Options{})
	s2 := e2.user("a")
	id2 := e2.register("App", cb)
	e2.connect(&s2, id2)
	e2.now = e2.now.Add(48 * time.Hour)
	s2 = e2.refreshSession(s2)
	if resp, _ := e2.get(authQuery(id2, cb), &s2); resp.StatusCode != 200 {
		t.Fatalf("authorized client: %d", resp.StatusCode)
	}
}

func TestOAuthEndpointsRateLimited(t *testing.T) {
	e := newEnv(t, Options{})
	for _, path := range []string{"/oauth/token", "/oauth/register", "/oauth/authorize"} {
		method := "POST"
		if path == "/oauth/authorize" {
			method = "GET"
		}
		var last int
		for i := 0; i < 31; i++ {
			req := httptest.NewRequest(method, path, strings.NewReader("{}"))
			req.RemoteAddr = "203.0.113.9:4444"
			req.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d", i+1)) // spoofing must not help
			rec := httptest.NewRecorder()
			e.h.ServeHTTP(rec, req)
			last = rec.Code
			if i < 30 && last == http.StatusTooManyRequests {
				t.Fatalf("%s limited early at %d", path, i)
			}
		}
		if last != http.StatusTooManyRequests {
			t.Fatalf("%s: 31st got %d", path, last)
		}
	}
}

func TestTokensNotLogged(t *testing.T) {
	e := newEnv(t, Options{})
	s := e.user("a")
	id := e.register("App", cb)
	tk := e.connect(&s, id)
	_, t2 := e.refresh(tk.Refresh, id)
	e.refresh(tk.Refresh, id) // reuse path
	e.mcp(t2.Access)
	logs := e.logs.String()
	for _, tok := range []string{tk.Access, tk.Refresh, t2.Access, t2.Refresh} {
		if strings.Contains(logs, tok) {
			t.Fatal("token appears in logs")
		}
	}
}

func TestConsentButtonRoles(t *testing.T) {
	ts, hc := cimdServer(t, func(u string) string {
		return fmt.Sprintf(`{"client_id":%q,"client_name":"Claude","redirect_uris":["https://claude.example/cb"]}`, u)
	})
	e := newEnv(t, Options{CIMDClient: hc})
	s := e.user("a")
	btn := func(body, value string) (class, text string, pos int, focus bool) {
		re := regexp.MustCompile(`<button class="([^"]*)" name="decision" value="` + value + `"([^>]*)>([^<]*)</button>`)
		loc := re.FindStringSubmatchIndex(body)
		if loc == nil {
			t.Fatalf("no %s button: %s", value, body)
		}
		m := re.FindStringSubmatch(body)
		return m[1], m[3], loc[0], strings.Contains(m[2], "autofocus")
	}
	_, body := e.consent(&s, e.register("ChatGPT", "https://evil.example/cb"), "https://evil.example/cb")
	dc, _, dp, df := btn(body, "deny")
	ac, at, ap, af := btn(body, "allow")
	if dc != "btn" || ac != "btn-secondary" || at != "Allow anyway" || dp > ap || !df || af {
		t.Fatalf("unverified roles: deny=%q allow=%q %q order=%v/%v focus=%v/%v", dc, ac, at, dp, ap, df, af)
	}
	_, body = e.consent(&s, ts.URL+"/client.json", "https://claude.example/cb")
	dc, _, dp, df = btn(body, "deny")
	ac, at, ap, af = btn(body, "allow")
	if ac != "btn" || dc != "btn-secondary" || at != "Allow" || ap > dp || df {
		t.Fatalf("verified roles: allow=%q deny=%q %q", ac, dc, at)
	}
}
