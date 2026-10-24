package web

import (
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

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
	csrf := e.connCSRF()
	// The page offers both choices with the exact count (1 memory + 0 others).
	_, page := e.get("/connections")
	if !strings.Contains(page, "Revoke, keep what it wrote") || !strings.Contains(page, "Revoke and delete 1 item it wrote") {
		t.Fatalf("two-choice step missing: %s", page)
	}
	// No choice, or a stale count: nothing happens.
	for _, f := range []url.Values{{}, {"choice": {"delete"}}, {"choice": {"delete"}, "confirm_count": {"7"}}, {"remove": {"1"}}} {
		f.Set("csrf", csrf)
		r, _ := e.post("/connections/"+conn.ID+"/revoke", f)
		if r.StatusCode == http.StatusSeeOther {
			t.Fatalf("revoked without a valid confirmation: %v", f)
		}
		if _, _, err := e.st.ResolveBearer(t.Context(), tok); err != nil {
			t.Fatalf("token revoked by refused request %v: %v", f, err)
		}
	}
	_, body := e.post("/connections/"+conn.ID+"/revoke", url.Values{"csrf": {csrf}, "choice": {"delete"}, "confirm_count": {"7"}})
	if !strings.Contains(body, "changed from 7 to 1") {
		t.Fatalf("stale count message: %s", body)
	}
	resp, _ := e.post("/connections/"+conn.ID+"/revoke", url.Values{"csrf": {csrf}, "choice": {"delete"}, "confirm_count": {"1"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status %d", resp.StatusCode)
	}
	// Persistent notice with Undo; Undo restores the item and the connection stays revoked.
	_, notice := e.get(resp.Header.Get("Location"))
	bm := regexpBatch.FindStringSubmatch(notice)
	if !strings.Contains(notice, "Revoked agent1.") || !strings.Contains(notice, "Undo delete") || bm == nil {
		t.Fatalf("no undo notice: %s", notice)
	}
	if r, _ := e.post("/memories/restore/"+bm[1], url.Values{"csrf": {csrf}, "next": {"/connections"}}); r.StatusCode != 303 || r.Header.Get("Location") != "/connections?restored=1" {
		t.Fatalf("undo: %d %q", r.StatusCode, r.Header.Get("Location"))
	}
	if n := len(e.liveContents(ua.ID)); n != 1 {
		t.Fatalf("undo left %d live memories", n)
	}
	if _, _, err := e.st.ResolveBearer(t.Context(), tok); err == nil {
		t.Fatal("undo re-enabled the token")
	}
	// Redo the delete part for the original assertions below.
	e.root.ForUser(ua.ID).SoftDeleteByConnection(t.Context(), conn.ID)
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
	r1, b1 := e.post("/connections/"+bconn.ID+"/revoke", url.Values{"csrf": {csrf}, "choice": {"delete"}, "confirm_count": {"0"}})
	r2, b2 := e.post("/connections/c_doesnotexist/revoke", url.Values{"csrf": {csrf}, "choice": {"delete"}, "confirm_count": {"0"}})
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
	const want = "You have access to my cortex memory. Before answering anything that depends on my preferences, how I'd do things, or people and things I know, call the cortex `context` tool. Whenever you learn something new about me (a preference, a person or organisation I know, a capability, my background), save it with cortex `remember`."
	if !strings.Contains(html.UnescapeString(body), want) {
		t.Fatalf("instruction text missing")
	}
}

func TestRevokeKeepLeavesWrittenData(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	ua := e.userBySub("g-alice")
	tok, conn, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "agent1")
	m := &cortex.Memory{Content: "kept"}
	if err := e.root.ForUser(ua.ID).WithConnection(conn.ID).PutMemory(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	resp, _ := e.post("/connections/"+conn.ID+"/revoke", url.Values{"csrf": {e.connCSRF()}, "choice": {"keep"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if _, _, err := e.st.ResolveBearer(t.Context(), tok); err == nil {
		t.Fatal("token still resolves")
	}
	if !e.liveContents(ua.ID)["kept"] {
		t.Fatal("kept memory was deleted")
	}
	_, body := e.get(resp.Header.Get("Location"))
	if !strings.Contains(body, "Revoked agent1.") || strings.Contains(body, "Undo delete") {
		t.Fatalf("notice wrong: %s", body)
	}
}

func TestTokenNameErrorTiedToField(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	resp, body := e.post("/connections/tokens", url.Values{"csrf": {e.connCSRF()}, "name": {"   "}})
	if resp.StatusCode != 200 || !strings.Contains(body, `aria-describedby="err-name"`) || !strings.Contains(body, `aria-invalid="true"`) || !strings.Contains(body, "Enter a name, up to 80") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
}

func TestAgo(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Second: "just now", time.Minute: "1 minute ago", 3 * time.Hour: "3 hours ago",
		72 * time.Hour: "3 days ago", 24 * time.Hour: "1 day ago", 70 * 24 * time.Hour: "2 months ago", 800 * 24 * time.Hour: "2 years ago",
	} {
		if got := ago(d); got != want {
			t.Errorf("ago(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestRenameConnection(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	ua := e.userBySub("g-alice")
	_, conn, err := e.st.CreateAgentToken(t.Context(), ua.ID, "old-name")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.root.ForUser(ua.ID).WithConnection(conn.ID).PutMemory(t.Context(), &cortex.Memory{Content: "from the agent"}); err != nil {
		t.Fatal(err)
	}
	csrf := e.connCSRF()
	// No CSRF: refused.
	if r, _ := e.post("/connections/"+conn.ID+"/rename", url.Values{"name": {"x"}}); r.StatusCode == http.StatusSeeOther {
		t.Fatal("rename without csrf")
	}
	// Validation: empty, blank, too long. Name stays.
	for _, bad := range []string{"", "   ", strings.Repeat("a", 81)} {
		r, body := e.post("/connections/"+conn.ID+"/rename", url.Values{"csrf": {csrf}, "name": {bad}})
		if r.StatusCode != http.StatusBadRequest || !strings.Contains(body, "1 to 80 characters") {
			t.Fatalf("bad name %q: %d", bad, r.StatusCode)
		}
	}
	// Trimmed on save.
	r, _ := e.post("/connections/"+conn.ID+"/rename", url.Values{"csrf": {csrf}, "name": {"  new-name  "}})
	if r.StatusCode != http.StatusSeeOther {
		t.Fatalf("rename: %d", r.StatusCode)
	}
	_, body := e.get(r.Header.Get("Location"))
	if !strings.Contains(body, "Renamed to new-name.") || strings.Contains(body, "old-name") {
		t.Fatalf("page after rename: %s", body)
	}
	_, mem := e.get("/memories")
	if !strings.Contains(mem, "new-name") || strings.Contains(mem, "old-name") {
		t.Fatalf("memories page does not show the new name: %s", mem)
	}
	// Absent id: 404.
	if r, _ := e.post("/connections/c_absent/rename", url.Values{"csrf": {csrf}, "name": {"x"}}); r.StatusCode != 404 {
		t.Fatalf("absent: %d", r.StatusCode)
	}
}

func TestConnectionRenameRemoveIsolation(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	ua := e.userBySub("g-alice")
	_, conn, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "alice-agent")
	if err := e.st.RevokeConnection(t.Context(), ua.ID, conn.ID); err != nil {
		t.Fatal(err)
	}
	e.newClient()
	e.signIn(Identity{Sub: "g-bob", Email: "bob@x.test", Name: "Bob", EmailVerified: true}, "")
	csrf := e.connCSRF()
	_, absent := e.post("/connections/c_absent/rename", url.Values{"csrf": {csrf}, "name": {"x"}})
	r1, foreign := e.post("/connections/"+conn.ID+"/rename", url.Values{"csrf": {csrf}, "name": {"hijack"}})
	r2, _ := e.post("/connections/"+conn.ID+"/remove", url.Values{"csrf": {csrf}})
	r3, _ := e.post("/connections/"+conn.ID+"/unremove", url.Values{"csrf": {csrf}})
	if r1.StatusCode != 404 || r2.StatusCode != 404 || r3.StatusCode != 404 || foreign != absent {
		t.Fatalf("foreign: %d %d %d", r1.StatusCode, r2.StatusCode, r3.StatusCode)
	}
	cs, _ := e.st.ListConnections(t.Context(), ua.ID)
	if cs[0].Name != "alice-agent" || cs[0].RemovedAt != nil {
		t.Fatalf("alice's connection changed: %+v", cs[0])
	}
}

func TestRemoveConnection(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	ua := e.userBySub("g-alice")
	tok, conn, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "gone-agent")
	if err := e.root.ForUser(ua.ID).WithConnection(conn.ID).PutMemory(t.Context(), &cortex.Memory{Content: "kept memory"}); err != nil {
		t.Fatal(err)
	}
	csrf := e.connCSRF()
	// Active: refused server-side.
	r, body := e.post("/connections/"+conn.ID+"/remove", url.Values{"csrf": {csrf}})
	if r.StatusCode != http.StatusConflict || !strings.Contains(body, "Revoke this connection before removing it") {
		t.Fatalf("active remove: %d", r.StatusCode)
	}
	if _, _, err := e.st.ResolveBearer(t.Context(), tok); err != nil {
		t.Fatal("token stopped working")
	}
	if _, page := e.get("/connections"); !strings.Contains(page, "gone-agent") || strings.Contains(page, "Remove from list") {
		t.Fatal("active row offers remove or vanished")
	}
	// Revoke (keep), then remove.
	if r, _ := e.post("/connections/"+conn.ID+"/revoke", url.Values{"csrf": {csrf}, "choice": {"keep"}}); r.StatusCode != 303 {
		t.Fatalf("revoke: %d", r.StatusCode)
	}
	if _, page := e.get("/connections"); !strings.Contains(page, "Remove from list") || !strings.Contains(page, "Hides it here. Memories it wrote are kept.") {
		t.Fatal("revoked row lacks Remove")
	}
	r, _ = e.post("/connections/"+conn.ID+"/remove", url.Values{"csrf": {csrf}})
	if r.StatusCode != 303 {
		t.Fatalf("remove: %d", r.StatusCode)
	}
	_, page := e.get(r.Header.Get("Location"))
	if !strings.Contains(page, "Removed gone-agent.") || !strings.Contains(page, "/unremove") {
		t.Fatalf("no undo notice: %s", page)
	}
	_, page = e.get("/connections")
	if strings.Contains(page, "gone-agent") {
		t.Fatal("removed connection still listed")
	}
	if _, _, err := e.st.ResolveBearer(t.Context(), tok); err == nil {
		t.Fatal("removed connection's token still valid")
	}
	// Memories keep the label; filter lists it as removed while it has memories.
	_, mem := e.get("/memories")
	if !strings.Contains(mem, "gone-agent (removed)") || !strings.Contains(mem, "gone-agent") {
		t.Fatalf("memories page: %s", mem)
	}
	// Undo restores it, still revoked.
	if r, _ := e.post("/connections/"+conn.ID+"/unremove", url.Values{"csrf": {csrf}}); r.StatusCode != 303 {
		t.Fatalf("unremove: %d", r.StatusCode)
	}
	_, page = e.get("/connections")
	if !strings.Contains(page, "gone-agent") || !strings.Contains(page, "Revoked") {
		t.Fatal("undo did not restore")
	}
	if _, _, err := e.st.ResolveBearer(t.Context(), tok); err == nil {
		t.Fatal("undo re-enabled token")
	}
}

func TestRemovedConnectionWithoutMemoriesLeavesFilter(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	ua := e.userBySub("g-alice")
	_, conn, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "empty-agent")
	_ = e.st.RevokeConnection(t.Context(), ua.ID, conn.ID)
	if err := e.st.RemoveConnection(t.Context(), ua.ID, conn.ID); err != nil {
		t.Fatal(err)
	}
	if _, mem := e.get("/memories"); strings.Contains(mem, "empty-agent") {
		t.Fatal("removed, memory-less connection still in filter")
	}
}

func TestConnectionsLeadsWithConnectAssistant(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	_, body := e.get("/connections")
	i, j, k := strings.Index(body, "Connect an assistant"), strings.Index(body, "Connected assistants"), strings.Index(body, "For your own scripts and agents")
	if i < 0 || !(i < j && j < k) {
		t.Fatalf("section order wrong: %d %d %d", i, j, k)
	}
	if !strings.Contains(body, "https://cortex.test/mcp") {
		t.Error("MCP address missing")
	}
	if strings.Contains(body, "Agent token") || strings.Contains(body, "agent token") {
		t.Error("jargon 'agent token' still shown")
	}
	if _, home := e.get("/"); strings.Contains(home, "You are set up.") {
		t.Error("set-up line shown before setup is done")
	}
}
