package web

import (
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/sausheong/cortex"
)

func init() { testMounts = append(testMounts, ConnectionsMount) }

func (e *env) connCSRF() string {
	e.t.Helper()
	_, body := e.get("/connections")
	m := csrfRe.FindStringSubmatch(body)
	if m == nil {
		e.t.Fatalf("no csrf: %.200s", body)
	}
	return m[1]
}

func TestConnectionsListsOwnOnly(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	bob := Identity{Sub: "g-bob", Email: "bob@x.test", Name: "Bob", EmailVerified: true}
	e.signIn(bob, "")
	ua, ub := e.userBySub("g-alice"), e.userBySub("g-bob")
	if _, err := e.st.CreateConnection(t.Context(), ua.ID, "oauth", "AliceAssistant", "https://a.test/c"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.st.CreateAgentToken(t.Context(), ua.ID, "alice-agent"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateConnection(t.Context(), ub.ID, "oauth", "BobAssistant", "https://b.test/c"); err != nil {
		t.Fatal(err)
	}
	// Signed in as bob (latest sign-in).
	_, body := e.get("/connections")
	if !strings.Contains(body, "BobAssistant") || strings.Contains(body, "AliceAssistant") || strings.Contains(body, "alice-agent") {
		t.Fatalf("bob's page wrong: %s", body)
	}
	e.newClient()
	e.signIn(alice(), "")
	_, body = e.get("/connections")
	if !strings.Contains(body, "AliceAssistant") || !strings.Contains(body, "alice-agent") || strings.Contains(body, "BobAssistant") {
		t.Fatalf("alice's page wrong: %s", body)
	}
}

func TestRevokeAndRemoveSoftDeletes(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	ua := e.userBySub("g-alice")
	tok, conn, err := e.st.CreateAgentToken(t.Context(), ua.ID, "agent1")
	if err != nil {
		t.Fatal(err)
	}
	m := &cortex.Memory{Content: "written by agent1"}
	if err := e.root.ForUser(ua.ID).WithConnection(conn.ID).PutMemory(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	conns, _ := e.st.ListConnections(t.Context(), ua.ID)
	if conns[0].Written != 1 {
		t.Fatalf("written = %d", conns[0].Written)
	}
	resp, _ := e.post("/connections/"+conn.ID+"/revoke", url.Values{"csrf": {e.connCSRF()}, "remove": {"1"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if _, _, err := e.st.ResolveBearer(t.Context(), tok); err == nil {
		t.Fatal("revoked token still resolves")
	}
	conns, _ = e.st.ListConnections(t.Context(), ua.ID)
	if conns[0].RevokedAt == nil || conns[0].Written != 0 {
		t.Fatalf("after remove: %+v", conns[0])
	}
}

func TestRevokeForeignConnectionIs404(t *testing.T) {
	e := newEnv(t)
	e.signIn(Identity{Sub: "g-bob", Email: "bob@x.test", Name: "Bob", EmailVerified: true}, "")
	ub := e.userBySub("g-bob")
	btok, bconn, err := e.st.CreateAgentToken(t.Context(), ub.ID, "bobagent")
	if err != nil {
		t.Fatal(err)
	}
	e.newClient()
	e.signIn(alice(), "")
	csrf := e.connCSRF()
	r1, b1 := e.post("/connections/"+bconn.ID+"/revoke", url.Values{"csrf": {csrf}, "remove": {"1"}})
	r2, b2 := e.post("/connections/c_doesnotexist/revoke", url.Values{"csrf": {csrf}, "remove": {"1"}})
	if r1.StatusCode != 404 || r2.StatusCode != 404 || b1 != b2 {
		t.Fatalf("foreign %d %q vs absent %d %q", r1.StatusCode, b1, r2.StatusCode, b2)
	}
	if uid, cid, err := e.st.ResolveBearer(t.Context(), btok); err != nil || uid != ub.ID || cid != bconn.ID {
		t.Fatalf("bob's token broken: %v", err)
	}
}

func TestNewTokenShownOnce(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	resp, body := e.post("/connections/tokens", url.Values{"csrf": {e.connCSRF()}, "name": {"laptop"}})
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	i := strings.Index(body, "ctx_")
	if i < 0 {
		t.Fatalf("no token: %s", body)
	}
	end := strings.IndexByte(body[i:], '<')
	tok := body[i : i+end]
	ua := e.userBySub("g-alice")
	if uid, _, err := e.st.ResolveBearer(t.Context(), tok); err != nil || uid != ua.ID {
		t.Fatalf("shown token does not resolve: %v", err)
	}
	_, again := e.get("/connections")
	if strings.Contains(again, tok) || !strings.Contains(again, "laptop") {
		t.Fatalf("token reshown or connection missing")
	}
}

func TestInstructionTextPresent(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	_, body := e.get("/connections")
	const want = "You have access to my cortex memory. Before answering anything that depends on my preferences, how I'd do things, or people and things I know, call the cortex `context` tool. Whenever you learn something new about me — a preference, a person or organisation I know, a capability, my background — save it with cortex `remember`."
	if !strings.Contains(html.UnescapeString(body), want) {
		t.Fatalf("instruction text missing")
	}
}
