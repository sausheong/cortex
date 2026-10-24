package web

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/webauthntest"
)

// Web tests run over httptest TLS, with the session set by the fake identity
// provider. The relying party is PublicURL (https://cortex.test), so the fake
// authenticator signs for that origin whatever address the test server has.

type pkWeb struct {
	e    *env
	dev  *webauthntest.Authenticator
	csrf string
	logs *bytes.Buffer
}

func newPKWeb(t *testing.T) *pkWeb {
	t.Helper()
	logs := &bytes.Buffer{}
	e := newEnv(t, func(c *Config) {
		c.EncryptionEnroll = true
		c.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	e.signIn(alice(), "")
	return &pkWeb{e: e, dev: webauthntest.New(t, "cortex.test", "https://cortex.test"), csrf: e.csrf(), logs: logs}
}

func (p *pkWeb) do(t *testing.T, path string, body any, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", p.e.ts.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if p.csrf != "" {
		req.Header.Set("X-CSRF-Token", p.csrf)
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := p.e.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, out
}

func (p *pkWeb) postJSON(t *testing.T, path string, body any) (*http.Response, []byte) {
	return p.do(t, path, body, nil)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

type enrolled struct {
	recovery string
	prf      []byte
	confirm  string
	pos      [2]int
}

// startEnroll runs begin and finish (the first half of enrollment).
func (p *pkWeb) startEnroll(t *testing.T) enrolled {
	t.Helper()
	resp, body := p.postJSON(t, "/enroll/begin", map[string]any{})
	if resp.StatusCode != 200 {
		t.Fatalf("begin: %d %s", resp.StatusCode, body)
	}
	var begin struct {
		Ceremony string          `json:"ceremony"`
		Options  json.RawMessage `json:"options"`
	}
	json.Unmarshal(body, &begin)
	prf := p.dev.PRFFromCreate(t, begin.Options)
	resp, body = p.postJSON(t, "/enroll/finish", map[string]any{
		"ceremony": begin.Ceremony, "attestation": json.RawMessage(p.dev.Create(t, begin.Options)),
		"prf_output": b64(prf), "name": "Laptop", "confirm_loss": true})
	if resp.StatusCode != 200 {
		t.Fatalf("finish: %d %s", resp.StatusCode, body)
	}
	var fin struct {
		Recovery  string `json:"recovery"`
		Confirm   string `json:"confirm"`
		Positions [2]int `json:"positions"`
	}
	json.Unmarshal(body, &fin)
	return enrolled{recovery: fin.Recovery, prf: prf, confirm: fin.Confirm, pos: fin.Positions}
}

func (en enrolled) groups() []string {
	g := strings.Split(en.recovery, "-")
	return []string{g[en.pos[0]-1], g[en.pos[1]-1]}
}

// confirm runs the proof round.
func (p *pkWeb) confirm(t *testing.T, en enrolled, prf []byte, groups []string) (*http.Response, []byte) {
	t.Helper()
	resp, body := p.postJSON(t, "/enroll/confirm/begin", map[string]any{"confirm": en.confirm})
	if resp.StatusCode != 200 {
		t.Fatalf("confirm begin: %d %s", resp.StatusCode, body)
	}
	var b struct {
		Options json.RawMessage `json:"options"`
	}
	json.Unmarshal(body, &b)
	assertion, realPRF := p.dev.Get(t, b.Options)
	if prf == nil {
		prf = realPRF
	}
	return p.postJSON(t, "/enroll/confirm", map[string]any{
		"confirm": en.confirm, "assertion": json.RawMessage(assertion), "prf_output": b64(prf), "groups": groups})
}

func (p *pkWeb) enrollAndConfirm(t *testing.T) enrolled {
	t.Helper()
	en := p.startEnroll(t)
	if resp, body := p.confirm(t, en, nil, en.groups()); resp.StatusCode != 200 {
		t.Fatalf("confirm: %d %s", resp.StatusCode, body)
	}
	return en
}

func (p *pkWeb) state(t *testing.T) string {
	t.Helper()
	var s string
	if err := p.e.st.DB.QueryRow(`SELECT encryption_state FROM users WHERE id = $1`, p.e.userBySub("g-alice").ID).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// unlockBegin returns the ceremony id, options and an assertion plus PRF.
func (p *pkWeb) unlockPrep(t *testing.T, hdr map[string]string) (resp *http.Response, ceremony string, assertion, prf []byte) {
	t.Helper()
	resp, body := p.do(t, "/unlock/begin", map[string]any{}, hdr)
	if resp.StatusCode != 200 {
		return resp, "", nil, nil
	}
	var b struct {
		Ceremony string          `json:"ceremony"`
		Options  json.RawMessage `json:"options"`
	}
	json.Unmarshal(body, &b)
	assertion, prf = p.dev.Get(t, b.Options)
	return resp, b.Ceremony, assertion, prf
}

func TestEnrollThroughHTTPFlipsStateOnlyAtConfirm(t *testing.T) {
	p := newPKWeb(t)
	en := p.startEnroll(t)
	if p.state(t) != "none" {
		t.Fatal("state changed before confirm")
	}
	resp, body := p.confirm(t, en, nil, en.groups())
	if resp.StatusCode != 200 || p.state(t) != "encrypted" {
		t.Fatalf("confirm: %d %s state=%s", resp.StatusCode, body, p.state(t))
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control %q", got)
	}
}

func TestEnrollRequiresLossConfirmation(t *testing.T) {
	p := newPKWeb(t)
	_, body := p.postJSON(t, "/enroll/begin", map[string]any{})
	var begin struct {
		Ceremony string          `json:"ceremony"`
		Options  json.RawMessage `json:"options"`
	}
	json.Unmarshal(body, &begin)
	for _, v := range []any{false, nil} {
		resp, _ := p.postJSON(t, "/enroll/finish", map[string]any{
			"ceremony": begin.Ceremony, "attestation": json.RawMessage(p.dev.Create(t, begin.Options)),
			"prf_output": b64(p.dev.PRFFromCreate(t, begin.Options)), "confirm_loss": v})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("confirm_loss=%v: %d, want 400", v, resp.StatusCode)
		}
	}
	var n int
	p.e.st.DB.QueryRow(`SELECT count(*) FROM user_key_wraps`).Scan(&n)
	if n != 0 {
		t.Fatal("wraps written without the loss confirmation")
	}
}

func TestEnrollRejectsMissingPRF(t *testing.T) {
	p := newPKWeb(t)
	_, body := p.postJSON(t, "/enroll/begin", map[string]any{})
	var begin struct {
		Ceremony string          `json:"ceremony"`
		Options  json.RawMessage `json:"options"`
	}
	json.Unmarshal(body, &begin)
	resp, out := p.postJSON(t, "/enroll/finish", map[string]any{
		"ceremony": begin.Ceremony, "attestation": json.RawMessage(p.dev.Create(t, begin.Options)),
		"confirm_loss": true})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(out), "PRF") {
		t.Fatalf("%d %s", resp.StatusCode, out)
	}
}

func TestEnrollBlockedWhenFlagOff(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	if r, _ := e.get("/enroll"); r.StatusCode != 404 {
		t.Fatalf("GET /enroll: %d", r.StatusCode)
	}
	p := &pkWeb{e: e, csrf: e.csrf()}
	if r, _ := p.postJSON(t, "/enroll/begin", map[string]any{}); r.StatusCode != 404 {
		t.Fatalf("POST /enroll/begin: %d", r.StatusCode)
	}
}

func TestEnrollPageForLegacyUser(t *testing.T) {
	p := newPKWeb(t)
	r, body := p.e.get("/enroll")
	if r.StatusCode != 200 || !strings.Contains(body, "Protect your memory with a passkey") ||
		!strings.Contains(body, "cannot be recovered") {
		t.Fatalf("%d %.300s", r.StatusCode, body)
	}
	// The script is loaded with the response's nonce and no handler is inline.
	csp := r.Header.Get("Content-Security-Policy")
	i := strings.Index(csp, "'nonce-")
	if i < 0 {
		t.Fatalf("no nonce in CSP: %s", csp)
	}
	nonce := csp[i+len("'nonce-") : i+len("'nonce-")+strings.Index(csp[i+len("'nonce-"):], "'")]
	if !strings.Contains(body, `<script src="/static/passkey.js" nonce="`+nonce+`" defer>`) {
		t.Fatalf("passkey.js not loaded with the nonce: %s", body)
	}
	if strings.Contains(body, "—") {
		t.Fatal("em dash in page")
	}
}

func TestLockedSessionShowsUnlockPage(t *testing.T) {
	p := newPKWeb(t)
	// A legacy user is not locked.
	if r, body := p.e.get("/memories"); r.StatusCode != 200 || strings.Contains(body, "Unlock with your passkey") {
		t.Fatalf("legacy user: %d", r.StatusCode)
	}
	p.enrollAndConfirm(t)
	for _, path := range []string{"/memories", "/graph", "/vault", "/settings"} {
		r, body := p.e.get(path)
		if r.StatusCode != 200 || !strings.Contains(body, "Unlock with your passkey") || !strings.Contains(body, "Use your recovery key instead") {
			t.Fatalf("%s: %d %.200s", path, r.StatusCode, body)
		}
	}
	// A state-changing request on a content route is refused outright.
	resp, _ := p.e.post("/memories/delete", url.Values{"csrf": {p.csrf}})
	if resp.StatusCode != http.StatusLocked {
		t.Fatalf("POST on a locked session: %d, want 423", resp.StatusCode)
	}
	// Account and sign-out stay reachable.
	if r, _ := p.e.get("/account"); r.StatusCode != 200 {
		t.Fatalf("/account: %d", r.StatusCode)
	}
}

func TestUnlockOverHTTP(t *testing.T) {
	p := newPKWeb(t)
	p.enrollAndConfirm(t)
	_, ceremony, assertion, prf := p.unlockPrep(t, nil)
	resp, body := p.postJSON(t, "/unlock/finish", map[string]any{
		"ceremony": ceremony, "assertion": json.RawMessage(assertion), "prf_output": b64(prf), "next": "/memories"})
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"next":"/memories"`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	// Wrong PRF: valid assertion, no unlock.
	_, ceremony, assertion, _ = p.unlockPrep(t, nil)
	resp, body = p.postJSON(t, "/unlock/finish", map[string]any{
		"ceremony": ceremony, "assertion": json.RawMessage(assertion), "prf_output": b64(make([]byte, 32))})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong PRF: %d %s", resp.StatusCode, body)
	}
}

func TestUnlockWithRecoveryOverHTTP(t *testing.T) {
	p := newPKWeb(t)
	en := p.enrollAndConfirm(t)
	resp, _ := p.e.post("/unlock/recovery", url.Values{"csrf": {p.csrf}, "recovery": {en.recovery}, "next": {"/memories"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/memories" {
		t.Fatalf("%d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, body := p.e.post("/unlock/recovery", url.Values{"csrf": {p.csrf}, "recovery": {"AAAA-BBBB"}})
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "That recovery key did not match.") {
		t.Fatalf("wrong key: %d %.200s", resp.StatusCode, body)
	}
}

func TestUnlockNextOpenRedirectRejected(t *testing.T) {
	p := newPKWeb(t)
	p.e.srv.unlockIP = accounts.NewLimiter(10000, 10000, nil)
	p.e.srv.unlockUser = accounts.NewLimiter(10000, 10000, nil)
	p.e.srv.recoveryUser = accounts.NewLimiter(10000, 10000, nil)
	en := p.enrollAndConfirm(t)
	bad := []string{"//evil.test/x", `/\evil.test`, "https://evil.test/x", "javascript:alert(1)", "evil.test", "/\t/evil.test", "/a\\b", ""}
	for _, next := range bad {
		resp, _ := p.e.post("/unlock/recovery", url.Values{"csrf": {p.csrf}, "recovery": {en.recovery}, "next": {next}})
		if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusSeeOther || loc != "/" {
			t.Errorf("recovery next=%q redirected to %q (%d), want /", next, loc, resp.StatusCode)
		}
		_, ceremony, assertion, prf := p.unlockPrep(t, nil)
		_, body := p.postJSON(t, "/unlock/finish", map[string]any{
			"ceremony": ceremony, "assertion": json.RawMessage(assertion), "prf_output": b64(prf), "next": next})
		if !strings.Contains(string(body), `"next":"/"`) {
			t.Errorf("finish next=%q returned %s, want next /", next, body)
		}
		// The page never carries the foreign target either.
		if _, page := p.e.get("/unlock?next=" + url.QueryEscape(next)); strings.Contains(page, "evil.test") {
			t.Errorf("unlock page reflects next=%q", next)
		}
	}
	// Same-origin relative paths survive, including a query.
	resp, _ := p.e.post("/unlock/recovery", url.Values{"csrf": {p.csrf}, "recovery": {en.recovery}, "next": {"/memories?q=a"}})
	if loc := resp.Header.Get("Location"); loc != "/memories?q=a" {
		t.Errorf("relative next lost: %q", loc)
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"/": "/", "/memories": "/memories", "/a/b?x=1#y": "/a/b?x=1#y",
		"": "/", "//x": "/", "/\\x": "/", "https://x": "/", "x": "/", "/a\nb": "/", "/%2f%2fevil": "/%2f%2fevil",
		"/" + strings.Repeat("a", 3000): "/",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnlockRateLimitedPerUserAndIP(t *testing.T) {
	p := newPKWeb(t)
	p.enrollAndConfirm(t)
	e := p.e
	begin := func(ip string) int {
		resp, _ := p.do(t, "/unlock/begin", map[string]any{}, map[string]string{"X-Forwarded-For": ip})
		return resp.StatusCode
	}
	// Per IP: the third request from one address is refused, another address is not.
	e.srv.unlockIP = accounts.NewLimiter(1, 2, func() time.Time { return e.now })
	e.srv.unlockUser = accounts.NewLimiter(1000, 1000, nil)
	if a, b, c := begin("10.0.0.1"), begin("10.0.0.1"), begin("10.0.0.1"); a != 200 || b != 200 || c != http.StatusTooManyRequests {
		t.Fatalf("per-IP: %d %d %d", a, b, c)
	}
	if got := begin("10.0.0.2"); got != 200 {
		t.Fatalf("other IP: %d", got)
	}
	// Per user: a changing address does not help.
	e.srv.unlockIP = accounts.NewLimiter(1000, 1000, nil)
	e.srv.unlockUser = accounts.NewLimiter(1, 2, func() time.Time { return e.now })
	if a, b, c := begin("10.0.1.1"), begin("10.0.1.2"), begin("10.0.1.3"); a != 200 || b != 200 || c != http.StatusTooManyRequests {
		t.Fatalf("per-user: %d %d %d", a, b, c)
	}
}

func TestRecoveryGuessesThrottled(t *testing.T) {
	p := newPKWeb(t)
	en := p.enrollAndConfirm(t)
	for i := 0; i < 5; i++ {
		resp, _ := p.e.post("/unlock/recovery", url.Values{"csrf": {p.csrf}, "recovery": {"AAAA-BBBB-CCCC"}})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("guess %d: %d", i, resp.StatusCode)
		}
	}
	// Even the right key is refused until the bucket refills.
	resp, _ := p.e.post("/unlock/recovery", url.Values{"csrf": {p.csrf}, "recovery": {en.recovery}})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("after 5 guesses: %d, want 429", resp.StatusCode)
	}
}

func TestCSRFRequiredOnEnrollFinishUnlockFinishRecovery(t *testing.T) {
	p := newPKWeb(t)
	en := p.enrollAndConfirm(t)
	for _, path := range []string{"/enroll/begin", "/enroll/finish", "/enroll/confirm/begin", "/enroll/confirm",
		"/unlock/begin", "/unlock/finish"} {
		for _, tok := range []string{"", "wrong"} {
			resp, _ := p.do(t, path, map[string]any{}, map[string]string{"X-CSRF-Token": tok})
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("POST %s with csrf %q: %d, want 403", path, tok, resp.StatusCode)
			}
		}
	}
	for _, tok := range []string{"", "wrong"} {
		resp, _ := p.e.post("/unlock/recovery", url.Values{"csrf": {tok}, "recovery": {en.recovery}})
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("recovery with csrf %q: %d, want 403", tok, resp.StatusCode)
		}
	}
	// No session at all: 401, not a redirect for a POST.
	p.e.newClient()
	resp, _ := p.do(t, "/unlock/finish", map[string]any{}, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no session: %d", resp.StatusCode)
	}
}

func TestPasskeyFlowsDoNotLogSecrets(t *testing.T) {
	p := newPKWeb(t)
	en := p.enrollAndConfirm(t)
	// Failures of every kind, including a hostile next=.
	p.e.post("/unlock/recovery", url.Values{"csrf": {p.csrf}, "recovery": {"WRONG-WRONG"}, "next": {"//secret-next.test"}})
	p.e.get("/unlock?next=//secret-next.test")
	_, ceremony, assertion, _ := p.unlockPrep(t, nil)
	p.postJSON(t, "/unlock/finish", map[string]any{
		"ceremony": ceremony, "assertion": json.RawMessage(assertion), "prf_output": b64(make([]byte, 32)), "next": "//secret-next.test"})
	p.postJSON(t, "/enroll/finish", map[string]any{"ceremony": "x", "attestation": json.RawMessage(`{"a":1}`), "prf_output": b64(en.prf), "confirm_loss": true})
	logs := p.logs.String()
	for _, needle := range []string{b64(en.prf), en.recovery, strings.ReplaceAll(en.recovery, "-", ""), "secret-next", "next=", "WRONG-WRONG", ceremony} {
		if needle != "" && strings.Contains(logs, needle) {
			t.Fatalf("logs contain %q", needle)
		}
	}
}

func TestPasskeyAssetsHaveNoEmDash(t *testing.T) {
	b, err := staticFS.ReadFile("static/passkey.js")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "—") {
		t.Fatal("em dash in passkey.js")
	}
}
