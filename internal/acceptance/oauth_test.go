package acceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/sausheong/cortex/internal/web"
	"github.com/sausheong/cortex/internal/web/oauth"
)

type stubIdP struct{}

func (stubIdP) AuthURL(state, nonce string) string { return "https://idp.test/" }
func (stubIdP) Exchange(context.Context, string, string) (web.Identity, error) {
	return web.Identity{}, fmt.Errorf("unused")
}

const (
	oauthCB  = "https://app.example/cb"
	verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
)

type oauthEnv struct {
	*env
	ts *httptest.Server
	hc *http.Client
}

func newOAuthEnv(t *testing.T) *oauthEnv {
	t.Helper()
	return serve(t, newEnv(t))
}

// serve mounts the web server (with OAuth) over e's database.
func serve(t *testing.T, e *env) *oauthEnv {
	t.Helper()
	h := web.NewServer(web.Config{PublicURL: "https://cortex.test", BootstrapEmail: "owner@x.test", Identity: stubIdP{}, EncryptionEnroll: true},
		e.root, e.st, web.Options{Mounts: []func(*http.ServeMux, web.Deps){oauth.Mount(oauth.Options{})}})
	o := &oauthEnv{env: e, ts: httptest.NewTLSServer(h)}
	t.Cleanup(o.ts.Close)
	o.hc = &http.Client{
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return o
}

func (o *oauthEnv) do(method, path string, cookies []*http.Cookie, body io.Reader, ctype string) (*http.Response, string) {
	o.t.Helper()
	req, _ := http.NewRequest(method, o.ts.URL+path, body)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := o.hc.Do(req)
	if err != nil {
		o.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func (o *oauthEnv) form(path string, cookies []*http.Cookie, v url.Values) (*http.Response, string) {
	return o.do("POST", path, cookies, strings.NewReader(v.Encode()), "application/x-www-form-urlencoded")
}

type tokens struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
}

var ridRe = regexp.MustCompile(`name="request_id" value="([^"]+)"`)

// connect runs register, consent and code exchange for an unlocked session.
func (o *oauthEnv) connect(cookies []*http.Cookie, csrf string) (tk tokens, clientID string) {
	o.t.Helper()
	b, _ := json.Marshal(map[string]any{"client_name": "App", "redirect_uris": []string{oauthCB}})
	resp, body := o.do("POST", "/oauth/register", nil, bytes.NewReader(b), "application/json")
	if resp.StatusCode != 201 {
		o.t.Fatalf("register: %d %s", resp.StatusCode, body)
	}
	var reg struct {
		ClientID string `json:"client_id"`
	}
	_ = json.Unmarshal([]byte(body), &reg)
	s := sha256.Sum256([]byte(verifier))
	q := url.Values{"response_type": {"code"}, "client_id": {reg.ClientID}, "redirect_uri": {oauthCB},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(s[:])}, "code_challenge_method": {"S256"}, "state": {"st1"}, "scope": {"cortex offline_access"}}
	resp, body = o.do("GET", "/oauth/authorize?"+q.Encode(), cookies, nil, "")
	m := ridRe.FindStringSubmatch(body)
	if resp.StatusCode != 200 || m == nil {
		o.t.Fatalf("consent: %d %s", resp.StatusCode, body)
	}
	resp, _ = o.form("/oauth/authorize", cookies, url.Values{"request_id": {m[1]}, "decision": {"allow"}, "csrf": {csrf}})
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || loc.Query().Get("code") == "" {
		o.t.Fatalf("no code: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, body = o.form("/oauth/token", nil, url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")},
		"client_id": {reg.ClientID}, "redirect_uri": {oauthCB}, "code_verifier": {verifier}})
	if resp.StatusCode != 200 {
		o.t.Fatalf("exchange: %d %s", resp.StatusCode, body)
	}
	_ = json.Unmarshal([]byte(body), &tk)
	return tk, reg.ClientID
}

func (o *oauthEnv) refresh(rt, clientID string) (*http.Response, tokens) {
	resp, body := o.form("/oauth/token", nil, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}, "client_id": {clientID}})
	var tk tokens
	_ = json.Unmarshal([]byte(body), &tk)
	return resp, tk
}

func (o *oauthEnv) oauthWraps() int {
	return o.count(`SELECT count(*) FROM user_key_wraps WHERE kind IN ('oauth_code','oauth_access','oauth_refresh')`)
}

func TestOAuthRefreshRewrapsCorrectly(t *testing.T) {
	o := newOAuthEnv(t)
	uid := o.user("zxqowner")
	en := o.enroll(uid)
	uk := en.Res.UK
	sessions := web.NewSessions(o.st, nil)
	sid, csrf, err := sessions.Create(bg, uid)
	if err != nil {
		t.Fatal(err)
	}
	secret, _, err := sessions.Unlock(bg, sid, uk)
	if err != nil {
		t.Fatal(err)
	}
	cookies := []*http.Cookie{{Name: "cortex_session", Value: sid}, {Name: "cortex_unlock", Value: secret}}
	tk, clientID := o.connect(cookies, csrf)
	if o.oauthWraps() != 2 {
		t.Fatalf("after connect: %d oauth wraps, want access + refresh", o.oauthWraps())
	}
	// The access token resolves to the user's own key.
	resolve := func(token string) error {
		u, _, k, err := o.st.ResolveBearer(bg, token)
		if err != nil {
			return err
		}
		defer k.Zero()
		if u != uid || !k.Equal(uk) {
			t.Fatalf("token resolved to user %q or a different key", u)
		}
		return nil
	}
	if err := resolve(tk.Access); err != nil {
		t.Fatalf("access token: %v", err)
	}
	resp, t2 := o.refresh(tk.Refresh, clientID)
	if resp.StatusCode != 200 {
		t.Fatalf("refresh: %d", resp.StatusCode)
	}
	if o.oauthWraps() != 2 {
		t.Fatalf("after refresh: %d oauth wraps, want 2 (old ones deleted)", o.oauthWraps())
	}
	if err := resolve(tk.Access); err == nil {
		t.Fatal("old access token still resolves")
	}
	if err := resolve(t2.Access); err != nil {
		t.Fatalf("new access token: %v", err)
	}
	if r, _ := o.refresh(tk.Refresh, clientID); r.StatusCode != 400 {
		t.Fatalf("old refresh token: %d", r.StatusCode)
	}
	// Refresh-token reuse revokes the family and deletes every wrap.
	if n := o.oauthWraps(); n != 0 {
		t.Fatalf("%d oauth wraps survive refresh-token reuse", n)
	}
}
