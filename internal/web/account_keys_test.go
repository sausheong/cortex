package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/webauthntest"
)

// --- reconnect ---

func TestEnrollmentMarksConnectionsNeedReconnect(t *testing.T) {
	p := newPKWeb(t)
	ctx := t.Context()
	uid := p.e.userBySub("g-alice").ID
	oldTok, agent := p.e.legacyToken(uid, "laptop-agent")
	oc, err := p.e.st.CreateConnection(ctx, uid, "oauth", "Claude", "client-1")
	if err != nil {
		t.Fatal(err)
	}
	gone, err := p.e.st.CreateConnection(ctx, uid, "oauth", "Old", "client-2")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.e.st.RevokeConnection(ctx, uid, gone.ID); err != nil {
		t.Fatal(err)
	}
	p.enrollAndConfirm(t)

	conns, _ := p.e.st.ListConnections(ctx, uid)
	flag := map[string]bool{}
	for _, c := range conns {
		flag[c.ID] = c.NeedsReconnect
	}
	if !flag[agent.ID] || !flag[oc.ID] || flag[gone.ID] {
		t.Fatalf("flags: %v", flag)
	}
	if _, _, _, err := p.e.st.ResolveBearer(ctx, oldTok); err == nil || err.Error() != model.ErrNeedsReconnect.Error() {
		t.Fatalf("old token: %v", err)
	}
	_, body := p.e.get("/connections")
	if !strings.Contains(body, ">Reconnect<") {
		t.Fatalf("no Reconnect action: %s", body)
	}
	// Reconnect issues a working new token, shown once.
	resp, page := p.e.post("/connections/"+agent.ID+"/renew", url.Values{"csrf": {p.csrf}})
	m := regexp.MustCompile(`ctx_[A-Za-z0-9_-]+`).FindString(page)
	if resp.StatusCode != 200 || m == "" {
		t.Fatalf("reconnect: %d %.300s", resp.StatusCode, page)
	}
	if _, _, uk, err := p.e.st.ResolveBearer(ctx, m); err != nil || uk == nil {
		t.Fatalf("new token: %v", err)
	} else {
		uk.Zero()
	}
	if _, _, _, err := p.e.st.ResolveBearer(ctx, oldTok); err == nil {
		t.Fatal("old token still works")
	}
	// The OAuth connection is told how to finish, with no dead-end action.
	if !strings.Contains(page, "sign in to cortex again") {
		t.Fatalf("oauth reconnect guidance missing: %s", page)
	}
}

func TestMarkNeedsReconnectOnlyLiveConnections(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	ctx := t.Context()
	uid := e.userBySub("g-alice").ID
	_, live, _ := e.st.CreateAgentToken(ctx, uid, "a", e.keys[uid])
	_, dead, _ := e.st.CreateAgentToken(ctx, uid, "b", e.keys[uid])
	e.st.RevokeConnection(ctx, uid, dead.ID)
	if err := e.st.MarkNeedsReconnect(ctx, uid); err != nil {
		t.Fatal(err)
	}
	conns, _ := e.st.ListConnections(ctx, uid)
	for _, c := range conns {
		if (c.ID == live.ID) != c.NeedsReconnect {
			t.Fatalf("%s flag=%v", c.Name, c.NeedsReconnect)
		}
	}
}

// --- passkeys ---

func credIDs(t *testing.T, p *pkWeb) []string {
	t.Helper()
	rows, err := p.e.st.DB.Query(`SELECT id FROM webauthn_credentials WHERE user_id = $1 ORDER BY created_at, id`, p.e.userBySub("g-alice").ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

func (p *pkWeb) addPasskey(t *testing.T, dev *webauthntest.Authenticator, name string) (*http.Response, []byte) {
	t.Helper()
	resp, body := p.postJSON(t, "/account/passkeys/add/begin", map[string]any{})
	if resp.StatusCode != 200 {
		return resp, body
	}
	var b struct {
		Ceremony string          `json:"ceremony"`
		Options  json.RawMessage `json:"options"`
	}
	json.Unmarshal(body, &b)
	return p.postJSON(t, "/account/passkeys/add/finish", map[string]any{
		"ceremony": b.Ceremony, "attestation": json.RawMessage(dev.Create(t, b.Options)),
		"prf_output": b64(dev.PRFFromCreate(t, b.Options)), "name": name})
}

func TestAccountAddRemovePasskeys(t *testing.T) {
	p := newPKWeb(t)
	p.enrollAndConfirm(t)
	dev2 := webauthntest.New(t, "cortex.test", "https://cortex.test")
	if resp, body := p.addPasskey(t, dev2, "Phone"); resp.StatusCode != 200 {
		t.Fatalf("add: %d %s", resp.StatusCode, body)
	}
	ids := credIDs(t, p)
	if len(ids) != 2 {
		t.Fatalf("credentials: %v", ids)
	}
	_, page := p.e.get("/account")
	if !strings.Contains(page, "Laptop") || !strings.Contains(page, "Phone") {
		t.Fatalf("account page does not list both passkeys: %s", page)
	}
	// Remove the first; the second still unlocks.
	resp, body := p.e.post("/account/passkeys/"+ids[0]+"/remove", url.Values{"csrf": {p.csrf}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("remove: %d %.300s", resp.StatusCode, body)
	}
	if left := credIDs(t, p); len(left) != 1 || left[0] != ids[1] {
		t.Fatalf("left: %v", left)
	}
	var n int
	p.e.st.DB.QueryRow(`SELECT count(*) FROM user_key_wraps WHERE kind='passkey' AND ref_id = $1`, ids[0]).Scan(&n)
	if n != 0 {
		t.Fatal("removed passkey kept its wrap")
	}
	p.e.dropCookie(unlockCookie)
	_, ub := p.do(t, "/unlock/begin", map[string]any{}, nil)
	var b struct {
		Ceremony string          `json:"ceremony"`
		Options  json.RawMessage `json:"options"`
	}
	json.Unmarshal(ub, &b)
	assertion, prf := dev2.Get(t, b.Options)
	fresp, fbody := p.postJSON(t, "/unlock/finish", map[string]any{"ceremony": b.Ceremony, "assertion": json.RawMessage(assertion), "prf_output": b64(prf)})
	if fresp.StatusCode != 200 {
		t.Fatalf("unlock with remaining passkey: %d %s", fresp.StatusCode, fbody)
	}
}

func TestAddPasskeyNeedsUnlockedSession(t *testing.T) {
	p := newPKWeb(t)
	p.enrollAndConfirm(t)
	p.e.dropCookie(unlockCookie)
	resp, _ := p.postJSON(t, "/account/passkeys/add/begin", map[string]any{})
	if resp.StatusCode != http.StatusLocked {
		t.Fatalf("locked add: %d, want 423", resp.StatusCode)
	}
}

func TestRemoveLastPasskeyRequiresRecovery(t *testing.T) {
	p := newPKWeb(t)
	en := p.enrollAndConfirm(t)
	ids := credIDs(t, p)
	for _, rec := range []string{"", "AAAA-BBBB-CCCC"} {
		resp, body := p.e.post("/account/passkeys/"+ids[0]+"/remove", url.Values{"csrf": {p.csrf}, "recovery": {rec}})
		if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "recovery key") {
			t.Fatalf("recovery %q: %d %.300s", rec, resp.StatusCode, body)
		}
		if len(credIDs(t, p)) != 1 {
			t.Fatal("last passkey removed without the recovery key")
		}
	}
	resp, body := p.e.post("/account/passkeys/"+ids[0]+"/remove", url.Values{"csrf": {p.csrf}, "recovery": {en.recovery}})
	if resp.StatusCode != http.StatusSeeOther || len(credIDs(t, p)) != 0 {
		t.Fatalf("with recovery: %d %.300s", resp.StatusCode, body)
	}
	// The recovery key still opens the account.
	p.e.dropCookie(unlockCookie)
	if resp, _ := p.e.post("/unlock/recovery", url.Values{"csrf": {p.csrf}, "recovery": {en.recovery}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("recovery unlock: %d", resp.StatusCode)
	}
}

// --- recovery key ---

var newRecoveryRe = regexp.MustCompile(`id="new-recovery"[^>]*>([^<]+)<`)

func TestRegenerateRecoveryNeedsUnlock(t *testing.T) {
	p := newPKWeb(t)
	en := p.enrollAndConfirm(t)
	uid := p.e.userBySub("g-alice").ID
	tick := url.Values{"csrf": {p.csrf}, "confirm_loss": {"on"}}

	p.e.dropCookie(unlockCookie)
	if resp, _ := p.e.post("/account/recovery/regenerate", tick); resp.StatusCode != http.StatusLocked {
		t.Fatalf("locked: %d, want 423", resp.StatusCode)
	}
	if uk, err := p.e.st.UnlockWithRecovery(t.Context(), uid, en.recovery); err != nil {
		t.Fatalf("old key changed while locked: %v", err)
	} else {
		uk.Zero()
	}
	// Unlocked but the confirmation is not ticked again: refused, old key kept.
	if resp, _ := p.e.post("/unlock/recovery", url.Values{"csrf": {p.csrf}, "recovery": {en.recovery}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("unlock: %d", resp.StatusCode)
	}
	resp, body := p.e.post("/account/recovery/regenerate", url.Values{"csrf": {p.csrf}})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "lost keys cannot be recovered") {
		t.Fatalf("unticked: %d %.300s", resp.StatusCode, body)
	}
	if uk, err := p.e.st.UnlockWithRecovery(t.Context(), uid, en.recovery); err != nil {
		t.Fatalf("old key lost without confirmation: %v", err)
	} else {
		uk.Zero()
	}
	resp, body = p.e.post("/account/recovery/regenerate", tick)
	m := newRecoveryRe.FindStringSubmatch(body)
	if resp.StatusCode != 200 || m == nil {
		t.Fatalf("regenerate: %d %.400s", resp.StatusCode, body)
	}
	fresh := strings.TrimSpace(m[1])
	if fresh == en.recovery {
		t.Fatal("same key")
	}
	if _, err := p.e.st.UnlockWithRecovery(t.Context(), uid, en.recovery); err == nil {
		t.Fatal("old recovery key still works")
	}
	if uk, err := p.e.st.UnlockWithRecovery(t.Context(), uid, fresh); err != nil {
		t.Fatalf("new key: %v", err)
	} else {
		uk.Zero()
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control %q", got)
	}
	// Shown once: a reload of the account page does not repeat it.
	if _, again := p.e.get("/account"); strings.Contains(again, fresh) {
		t.Fatal("recovery key shown again")
	}
}

// --- forget all devices ---

func TestForgetAllDevicesKeepsCurrentSession(t *testing.T) {
	p := newPKWeb(t)
	en := p.enrollAndConfirm(t)
	ctx := t.Context()
	uid := p.e.userBySub("g-alice").ID
	uk, err := p.e.st.UnlockWithRecovery(ctx, uid, en.recovery)
	if err != nil {
		t.Fatal(err)
	}
	defer uk.Zero()
	// Another device, an agent token and two OAuth wraps.
	sid2, _, _ := p.e.srv.sessions.Create(ctx, uid)
	if _, _, err := p.e.srv.sessions.Unlock(ctx, sid2, uk); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.e.st.CreateAgentToken(ctx, uid, "agent", uk); err != nil {
		t.Fatal(err)
	}
	oc, _ := p.e.st.CreateConnection(ctx, uid, "oauth", "Claude", "client-1")
	for i, kind := range []string{"oauth_access", "oauth_refresh"} {
		p.e.st.DB.Exec(`INSERT INTO oauth_tokens (token_hash, kind, grant_id, connection_id, user_id, expires_at) VALUES ($1,$2,'g',$3,$4, now() + interval '1 day')`,
			"h"+kind, map[bool]string{true: "access", false: "refresh"}[i == 0], oc.ID, uid)
		if err := p.e.st.PutWrap(ctx, accounts.Wrap{UserID: uid, Kind: kind, RefID: "h" + kind, Wrapped: []byte("x")}); err != nil {
			t.Fatal(err)
		}
	}
	if p.e.wrapCount("session") != 2 || p.e.wrapCount("agent") != 1 {
		t.Fatal("setup")
	}

	resp, body := p.e.post("/account/forget-devices", url.Values{"csrf": {p.csrf}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("forget: %d %.300s", resp.StatusCode, body)
	}
	var ref string
	if p.e.wrapCount("session") != 1 {
		t.Fatalf("session wraps: %d", p.e.wrapCount("session"))
	}
	p.e.st.DB.QueryRow(`SELECT ref_id FROM user_key_wraps WHERE kind='session'`).Scan(&ref)
	if ref != hashOf(p.e.cookieVal(sessionCookie)) {
		t.Fatal("kept the wrong session wrap")
	}
	if p.e.wrapCount("oauth_access") != 0 || p.e.wrapCount("oauth_refresh") != 0 {
		t.Fatal("oauth wraps kept")
	}
	if p.e.wrapCount("agent") != 1 {
		t.Fatal("agent token wrap touched")
	}
	var live int
	p.e.st.DB.QueryRow(`SELECT count(*) FROM oauth_tokens WHERE connection_id = $1 AND revoked_at IS NULL`, oc.ID).Scan(&live)
	if live != 0 {
		t.Fatal("oauth grants not revoked")
	}
	conns, _ := p.e.st.ListConnections(ctx, uid)
	for _, c := range conns {
		if c.ID == oc.ID && !c.NeedsReconnect {
			t.Fatal("oauth connection not flagged to reconnect")
		}
	}
	// The current session is still unlocked.
	if r, b := p.e.get("/settings"); r.StatusCode != 200 || strings.Contains(b, "Unlock with your passkey") {
		t.Fatalf("current session locked out: %d", r.StatusCode)
	}
}

func TestForgetAllDevicesDeletesSessionAndOAuthWraps(t *testing.T) {
	p := newPKWeb(t)
	en := p.enrollAndConfirm(t)
	ctx := t.Context()
	uid := p.e.userBySub("g-alice").ID
	uk, err := p.e.st.UnlockWithRecovery(ctx, uid, en.recovery)
	if err != nil {
		t.Fatal(err)
	}
	defer uk.Zero()
	sid2, _, _ := p.e.srv.sessions.Create(ctx, uid)
	if _, _, err := p.e.srv.sessions.Unlock(ctx, sid2, uk); err != nil {
		t.Fatal(err)
	}
	if err := p.e.st.PutWrap(ctx, accounts.Wrap{UserID: uid, Kind: "oauth_access", RefID: "a1", Wrapped: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	// A locked browser (its own session wrap gone) can still cut every other
	// device loose; with no wrap of its own to keep, no session wrap survives.
	p.e.dropCookie(unlockCookie)
	p.e.st.DB.Exec(`DELETE FROM user_key_wraps WHERE kind = 'session' AND ref_id = $1`, hashOf(p.e.cookieVal(sessionCookie)))
	if r, _ := p.e.post("/account/forget-devices", url.Values{"csrf": {p.e.csrfFromAccount()}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("forget: %d", r.StatusCode)
	}
	if p.e.wrapCount("session") != 0 || p.e.wrapCount("oauth_access") != 0 {
		t.Fatalf("wraps left: session=%d oauth_access=%d", p.e.wrapCount("session"), p.e.wrapCount("oauth_access"))
	}
	if p.e.wrapCount("passkey") != 1 || p.e.wrapCount("recovery") != 1 {
		t.Fatal("passkey or recovery wrap touched")
	}
}

// --- CSRF and copy ---

func TestAccountPostsRequireCSRF(t *testing.T) {
	p := newPKWeb(t)
	p.enrollAndConfirm(t)
	ids := credIDs(t, p)
	for _, path := range []string{
		"/account/passkeys/add/begin", "/account/passkeys/add/finish",
		"/account/passkeys/" + ids[0] + "/remove", "/account/recovery/regenerate",
		"/account/forget-devices", "/account/delete",
	} {
		for _, tok := range []string{"", "wrong"} {
			hdr := map[string]string{"X-CSRF-Token": tok}
			resp, _ := p.do(t, path, map[string]any{}, hdr)
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("POST %s json csrf %q: %d, want 403", path, tok, resp.StatusCode)
			}
			resp, _ = p.e.post(path, url.Values{"csrf": {tok}, "confirm_loss": {"on"}})
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("POST %s form csrf %q: %d, want 403", path, tok, resp.StatusCode)
			}
		}
	}
	if len(credIDs(t, p)) != 1 || p.state(t) != "encrypted" {
		t.Fatal("a rejected post changed state")
	}
}

func TestAccountPageCopyHasNoEmDash(t *testing.T) {
	p := newPKWeb(t)
	p.enrollAndConfirm(t)
	_, body := p.e.get("/account")
	if strings.Contains(body, "—") {
		t.Fatal("em dash on the account page")
	}
	for _, want := range []string{"Passkeys", "Recovery key", "Forget all devices", "cannot be recovered"} {
		if !strings.Contains(body, want) {
			t.Errorf("account page lacks %q", want)
		}
	}
}
