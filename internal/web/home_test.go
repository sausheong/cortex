package web

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"
)

type countLLM struct {
	calls atomic.Int32
	out   string
}

func (l *countLLM) Extract(context.Context, string, string) (cortex.ExtractionResult, error) {
	l.calls.Add(1)
	return cortex.ExtractionResult{}, nil
}
func (l *countLLM) Decompose(context.Context, string) ([]cortex.StructuredQuery, error) {
	l.calls.Add(1)
	return nil, nil
}
func (l *countLLM) Summarize(context.Context, []string) (string, error) {
	l.calls.Add(1)
	return l.out, nil
}

func (e *env) setProvider(uid string) {
	e.t.Helper()
	s := accounts.Settings{BaseURL: "https://api.example.com/v1", APIKey: "k1", ChatModel: "gpt-test", EmbedModel: "e", EmbedDims: 512}
	if _, err := e.st.PutSettings(e.t.Context(), uid, s); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) write(uid, connID, text string) {
	e.t.Helper()
	if err := e.root.ForUser(uid).WithConnection(connID).PutMemory(e.t.Context(), &cortex.Memory{Content: text}); err != nil {
		e.t.Fatal(err)
	}
}

func TestHomeSignedOutShowsSignIn(t *testing.T) {
	e := newEnv(t)
	r, body := e.get("/")
	if r.StatusCode != 200 || !strings.Contains(body, "Sign in") {
		t.Fatalf("signed out: %d %.200s", r.StatusCode, body)
	}
	if strings.Contains(body, `href="/memories"`) {
		t.Fatal("nav shown to signed-out visitor")
	}
}

func TestHomeSignedInRendersHomeWithNav(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	r, body := e.get("/")
	if r.StatusCode != 200 {
		t.Fatalf("status %d", r.StatusCode)
	}
	for _, want := range []string{"<h1>Home</h1>", `href="/" aria-current="page">Home`, "Get started"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(body, `href="/memories" aria-current`) {
		t.Error("memories marked current on home")
	}
}

func TestFirstRunNavShrinksAndChecklistLinks(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	_, body := e.get("/")
	nav := body[strings.Index(body, `<nav aria-label="Main">`):strings.Index(body, "</nav>")]
	for _, want := range []string{`href="/"`, `href="/settings"`, "Sign out"} {
		if !strings.Contains(nav, want) {
			t.Errorf("first-run nav missing %q", want)
		}
	}
	for _, hidden := range []string{`href="/memories"`, `href="/graph"`, `href="/vault"`, `href="/connections"`, `href="/account"`} {
		if strings.Contains(nav, hidden) {
			t.Errorf("first-run nav should hide %q", hidden)
		}
	}
	// Every step links somewhere.
	for _, want := range []string{`href="/settings">Open settings`, `href="/connections">Connect one`, `href="/memories">See Memories`} {
		if !strings.Contains(body, want) {
			t.Errorf("checklist missing link %q", want)
		}
	}
	// The page you are on keeps its nav item, on any signed-in page.
	_, body = e.get("/connections")
	if !strings.Contains(body, `href="/connections" aria-current="page"`) {
		t.Error("current page must stay in the shrunken nav")
	}
	_, body = e.get("/graph")
	if strings.Contains(body[strings.Index(body, `<nav aria-label="Main">`):strings.Index(body, "</nav>")], `href="/memories"`) {
		t.Error("nav should be shrunk on pages without the checklist too")
	}
	// Once set up the full nav returns.
	ua := e.userBySub("g-alice")
	e.setProvider(ua.ID)
	_, conn, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "agent", nil)
	e.write(ua.ID, conn.ID, "likes tea")
	e.st.DB.ExecContext(t.Context(), `UPDATE connections SET last_used = $2 WHERE id = $1`, conn.ID, time.Now())
	_, body = e.get("/")
	nav = body[strings.Index(body, `<nav aria-label="Main">`):strings.Index(body, "</nav>")]
	if strings.Index(nav, ">Home<") > strings.Index(nav, ">Memories<") || !strings.Contains(nav, `href="/account"`) {
		t.Error("full nav (Home first) should return after setup")
	}
}

func TestHomeFirstRunChecklistLeads(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	_, body := e.get("/")
	if !strings.Contains(body, "Get started") {
		t.Fatal("checklist should lead")
	}
	for _, hidden := range []string{"About you", "Nightly maintenance", "Runs overnight once", `class="home-strip"`, "Assistants</h2>", "Health</h2>", "Recently deleted", "Recently added"} {
		if strings.Contains(body, hidden) {
			t.Errorf("first run should hide %q", hidden)
		}
	}
	for _, want := range []string{"Set up your AI provider"} {
		if !strings.Contains(body, want) {
			t.Errorf("empty state missing %q", want)
		}
	}
	// Once everything is set up the checklist disappears.
	ua := e.userBySub("g-alice")
	e.setProvider(ua.ID)
	_, conn, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "agent", nil)
	e.write(ua.ID, conn.ID, "likes tea")
	e.st.DB.ExecContext(t.Context(), `UPDATE connections SET last_used = $2 WHERE id = $1`, conn.ID, time.Now())
	_, body = e.get("/")
	if strings.Contains(body, "Get started") {
		t.Fatal("checklist still shown when setup is complete")
	}
	if !strings.Contains(body, "You are set up.") {
		t.Error("complete setup should show the quiet set-up line")
	}
	for _, want := range []string{"About you", "Everything is working", `class="home-strip"`, "1 assistant active"} {
		if !strings.Contains(body, want) {
			t.Errorf("complete setup should show %q", want)
		}
	}
}

func TestHomeNewSinceGroupsAndLinks(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	ua := e.userBySub("g-alice")
	e.setProvider(ua.ID)
	_, chat, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "ChatGPT", nil)
	_, other, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "Claude", nil)
	// First visit stamps the clock.
	e.get("/")
	for _, m := range []string{"one", "two", "three", "four"} {
		e.write(ua.ID, chat.ID, "chat says "+m)
	}
	e.write(ua.ID, other.ID, "claude says hi")
	e.now = e.now.Add(time.Hour)
	_, body := e.get("/")
	if !strings.Contains(body, "New since your last visit") {
		t.Fatalf("heading: %s", body)
	}
	if !strings.Contains(body, "5 new memories since your last visit") {
		t.Fatalf("strip total: %s", body)
	}
	if !strings.Contains(body, "ChatGPT &middot; 4 new") {
		t.Fatalf("ChatGPT group missing: %s", body)
	}
	if !strings.Contains(body, "Claude &middot; 1 new") {
		t.Fatal("Claude group missing")
	}
	// One line per group: the newest item.
	if n := strings.Count(body, "chat says"); n != 1 {
		t.Fatalf("lines for ChatGPT = %d, want 1", n)
	}
	since := e.now.Add(-time.Hour).Truncate(time.Second).UTC().Format(time.RFC3339)
	want := `href="/memories?by=` + chat.ID + `&amp;since=` + url.QueryEscape(since) + `&amp;sort=newest"`
	if !strings.Contains(body, want) {
		t.Fatalf("link %q missing in %s", want, body)
	}
	if strings.Contains(body, "from=") {
		t.Fatal("new-since links must use an exact timestamp, not a day")
	}
}

// The Memories page behind a "N new" link shows exactly N memories, including
// ones written earlier the same day, and "Added by you or imported" links to by=none.
func TestHomeNewSinceLinksMatchCounts(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	ua := e.userBySub("g-alice")
	_, chat, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "ChatGPT", nil)
	// Written before the visit marker, same calendar day.
	e.write(ua.ID, chat.ID, "old chat note")
	if err := e.root.ForUser(ua.ID).PutMemory(t.Context(), &cortex.Memory{Content: "old direct note"}); err != nil {
		t.Fatal(err)
	}
	// The visit marker is whole seconds: put it in a later second than the old notes.
	time.Sleep(1100 * time.Millisecond)
	e.now = time.Now().UTC()
	e.get("/")
	e.now = e.now.Add(time.Hour)
	e.write(ua.ID, chat.ID, "new chat one")
	e.write(ua.ID, chat.ID, "new chat two")
	if err := e.root.ForUser(ua.ID).PutMemory(t.Context(), &cortex.Memory{Content: "new direct note"}); err != nil {
		t.Fatal(err)
	}
	_, body := e.get("/")
	hrefs := regexp.MustCompile(`<strong class="row-name"><a href="(/memories\?[^"]+)">([^<]*)`).FindAllStringSubmatch(body, -1)
	if len(hrefs) != 2 {
		t.Fatalf("want 2 groups, got %v in %s", hrefs, body)
	}
	for _, h := range hrefs {
		link := html.UnescapeString(h[1])
		_, page := e.get(link)
		n := regexp.MustCompile(`(\d+) new`).FindStringSubmatch(h[2])[1]
		want := n + " " + map[string]string{"1": "memory", "2": "memories"}[n]
		if !strings.Contains(page, `<p class="lede">`+want) {
			t.Errorf("%s: %q does not show %s: %.400s", link, h[2], want, page)
		}
		if strings.Contains(page, "old chat note") || strings.Contains(page, "old direct note") {
			t.Errorf("%s: lists memories from before the visit", link)
		}
		if strings.HasPrefix(h[2], "Added by you or imported") && !strings.Contains(link, "by=none") {
			t.Errorf("Added by you or imported must link with by=none: %s", link)
		}
	}
}

// Removed connections do not tick the setup checklist's "Connect" step.
func TestSetupIgnoresRemovedConnections(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	ua := e.userBySub("g-alice")
	_, conn, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "Gone", nil)
	if st := e.srv.setupState(t.Context(), ua.ID); st == nil || !st.Connect {
		t.Fatalf("live connection should tick Connect: %+v", st)
	}
	if _, err := e.st.DB.ExecContext(t.Context(), `UPDATE connections SET removed_at = now() WHERE id = $1`, conn.ID); err != nil {
		t.Fatal(err)
	}
	if st := e.srv.setupState(t.Context(), ua.ID); st == nil || st.Connect {
		t.Fatalf("removed connection must not tick Connect: %+v", st)
	}
}

func TestHomeLastVisitUpdatesAfterViewing(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	ua := e.userBySub("g-alice")
	_, conn, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "Agent", nil)
	e.get("/")
	e.write(ua.ID, conn.ID, "fresh note")
	// A reload inside the visit gap does not count as a new visit.
	e.now = e.now.Add(5 * time.Minute)
	e.get("/")
	e.now = e.now.Add(time.Hour)
	_, body := e.get("/")
	if !strings.Contains(body, "fresh note") {
		t.Fatalf("visit 2 should list the note: %s", body)
	}
	// Viewing it moved the marker: the next visit has nothing new.
	e.now = e.now.Add(time.Hour)
	_, body = e.get("/")
	if strings.Contains(body, "fresh note") || !strings.Contains(body, "Nothing new since") {
		t.Fatalf("visit 3 should be empty: %s", body)
	}
}

func TestHomeAssistantsFlags(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	ua := e.userBySub("g-alice")
	_, fresh, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "FreshOne", nil)
	_, idle, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "IdleOne", nil)
	_, _, _ = e.st.CreateAgentToken(t.Context(), ua.ID, "NeverOne", nil)
	_, gone, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "GoneOne", nil)
	if err := e.st.RevokeConnection(t.Context(), ua.ID, gone.ID); err != nil {
		t.Fatal(err)
	}
	db := e.st.DB
	db.ExecContext(t.Context(), `UPDATE connections SET last_used = $2 WHERE id = $1`, fresh.ID, time.Now().Add(-time.Hour))
	db.ExecContext(t.Context(), `UPDATE connections SET last_used = $2 WHERE id = $1`, idle.ID, time.Now().Add(-45*24*time.Hour))
	e.write(ua.ID, fresh.ID, "a")
	e.write(ua.ID, fresh.ID, "b")
	_, body := e.get("/")
	for _, want := range []string{"FreshOne", "IdleOne", "NeverOne", "Not used yet", "Idle (no use for 30+ days)", "2 written in the last 7 days", "0 written in the last 7 days"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(body, "GoneOne") {
		t.Error("revoked connection listed")
	}
	if strings.Count(body, "Idle (no use for 30+ days)") != 1 {
		t.Error("only the idle connection is flagged")
	}
}

func TestHomeHealthRows(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	ua := e.userBySub("g-alice")
	e.setProvider(ua.ID)
	_, conn, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "agent", nil)
	e.write(ua.ID, conn.ID, "seed")
	_, body := e.get("/")
	// Provider configured and nothing wrong: one line, and no advice to set up
	// a provider that is already set up.
	if !strings.Contains(body, "Everything is working.") || strings.Contains(body, "once your AI provider") || strings.Contains(body, "Nightly maintenance") {
		t.Errorf("healthy state should collapse to one line: %s", body)
	}
	cx := e.root.ForUser(ua.ID)
	if _, err := cx.RefreshUsage(t.Context()); err != nil {
		t.Fatal(err)
	}
	set := func(msg any) {
		e.st.DB.ExecContext(t.Context(), `UPDATE users SET last_maintenance_at = $2, last_maintenance_error = $3 WHERE id = $1`, ua.ID, e.now.Add(-3*time.Hour), msg)
	}
	set(nil)
	_, body = e.get("/")
	if !strings.Contains(body, "maintenance ran 3 hours ago.") || strings.Contains(body, "Nightly maintenance") {
		t.Errorf("ok state: %s", body)
	}
	set("maintenance completed; 2 item(s) skipped after provider errors")
	_, body = e.get("/")
	if !strings.Contains(body, "2 items were skipped") || !strings.Contains(body, "Configured with gpt-test") || strings.Contains(body, "Everything is working") {
		t.Error("partial state")
	}
	set("provider settings could not be loaded")
	_, body = e.get("/")
	if !strings.Contains(body, "Failed 3 hours ago: provider settings could not be loaded.") {
		t.Errorf("failed state: %s", body)
	}
	// Deleted items are counted with a link to restore them.
	m := &cortex.Memory{Content: "to delete"}
	if err := cx.PutMemory(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cx.SoftDeleteMemories(t.Context(), cortex.DeleteFilter{IDs: []string{m.ID}}, "test"); err != nil {
		t.Fatal(err)
	}
	_, body = e.get("/")
	if !strings.Contains(body, "can still be restored for 30 days") || !strings.Contains(body, `href="/memories/deleted"`) {
		t.Errorf("deleted row: %s", body)
	}
}

func TestHomeProfileFromCacheOnly(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	ua := e.userBySub("g-alice")
	e.setProvider(ua.ID)
	cx := e.root.ForUser(ua.ID)
	owner, err := cx.OwnerEntityID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := cx.PutMemory(t.Context(), &cortex.Memory{Content: "Alice likes tea", Static: true, EntityIDs: []string{owner}}); err != nil {
		t.Fatal(err)
	}
	e.st.CreateAgentToken(t.Context(), ua.ID, "agent", nil)
	// Not built yet: plain line, link kept.
	_, body := e.get("/")
	if !strings.Contains(body, "builds overnight") || !strings.Contains(body, `href="/graph"`) {
		t.Fatalf("pending profile: %s", body)
	}
	// Build one with a stub, as the nightly job would.
	build := &countLLM{out: "- Likes tea\n- Lives in Singapore\n- Works in data\n- Runs on weekends\n- Plays piano\n- Reads a lot\n- Has two cats"}
	if _, err := cx.ForUser(ua.ID, cortex.WithLLM(build)).Profile(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	// Page load with a counting LLM and provider test: neither is called.
	spy := &countLLM{out: "- nope"}
	e.srv.root = e.root.ForUser("", cortex.WithLLM(spy))
	var tests atomic.Int32
	e.srv.testProvider = func(context.Context, accounts.Settings) error { tests.Add(1); return nil }
	enq := len(e.enq)
	_, body = e.get("/")
	if spy.calls.Load() != 0 || tests.Load() != 0 || len(e.enq) != enq {
		t.Fatalf("page load called providers: llm=%d test=%d enqueue=%d", spy.calls.Load(), tests.Load(), len(e.enq)-enq)
	}
	for _, want := range []string{"Likes tea", "Plays piano", "Show 2 more", "Reads a lot", "Explore your graph"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in %s", want, body)
		}
	}
}

func TestHomeIsolation(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	ua := e.userBySub("g-alice")
	e.setProvider(ua.ID)
	_, ac, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "AliceBot", nil)
	e.write(ua.ID, ac.ID, "alice secret memory")
	e.get("/")
	e.newClient()
	bob := Identity{Sub: "g-bob", Email: "bob@x.test", Name: "Bob", EmailVerified: true}
	e.signIn(bob, "")
	r, body := e.get("/")
	if r.StatusCode != http.StatusOK {
		t.Fatal(r.StatusCode)
	}
	for _, leak := range []string{"alice secret memory", "AliceBot", "gpt-test", "alice@x.test"} {
		if strings.Contains(body, leak) {
			t.Errorf("bob sees %q", leak)
		}
	}
	if !strings.Contains(body, "Get started") {
		t.Error("bob should be in first-run")
	}
	// Alice's visit cookie does not carry over to Bob in the same browser.
	e.write(e.userBySub("g-bob").ID, "", "bob note")
	e.now = e.now.Add(time.Hour)
	_, body = e.get("/")
	if !strings.Contains(body, "bob note") {
		t.Errorf("bob's own note missing: %s", body)
	}
}

func TestHomeStatusNeverContradicts(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	ua := e.userBySub("g-alice")
	e.setProvider(ua.ID)

	// None connected: no assistant clause and no set-up line.
	e.write(ua.ID, "", "likes tea")
	_, body := e.get("/")
	if strings.Contains(body, "You are set up") || strings.Contains(body, "assistant active") || strings.Contains(body, "not used yet") {
		t.Fatalf("no assistants: unexpected status: %s", body)
	}

	// Connected but never used: honest strip, no set-up line.
	_, conn, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "agent", nil)
	_, body = e.get("/")
	if !strings.Contains(body, "1 assistant connected, not used yet") {
		t.Errorf("connected-but-unused strip missing: %s", body)
	}
	if strings.Contains(body, "You are set up") || strings.Contains(body, "assistant active") {
		t.Errorf("unused assistant must not read as set up or active: %s", body)
	}

	// Used: active in both places.
	e.st.DB.ExecContext(t.Context(), `UPDATE connections SET last_used = $2 WHERE id = $1`, conn.ID, time.Now())
	_, body = e.get("/")
	if !strings.Contains(body, "You are set up.") || !strings.Contains(body, "1 assistant active") {
		t.Errorf("active assistant should show set-up line and strip: %s", body)
	}
	if strings.Contains(body, "not used yet</span>") && strings.Contains(body, "connected, not used yet") {
		t.Errorf("contradiction: %s", body)
	}
}

// Home and Connections must agree on every assistant's state, and Home's
// strip must add up to the rows it lists.
func TestAssistantStatusAgreesAcrossPages(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	ua := e.userBySub("g-alice")
	e.setProvider(ua.ID)
	e.write(ua.ID, "", "seed")
	_, act, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "ActiveOne", nil)
	_, idle, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "IdleOne", nil)
	_, _, _ = e.st.CreateAgentToken(t.Context(), ua.ID, "NeverOne", nil)
	e.st.DB.ExecContext(t.Context(), `UPDATE connections SET last_used = $2 WHERE id = $1`, act.ID, e.now.Add(-time.Hour))
	e.st.DB.ExecContext(t.Context(), `UPDATE connections SET last_used = $2 WHERE id = $1`, idle.ID, e.now.Add(-45*24*time.Hour))

	_, home := e.get("/")
	_, conns := e.get("/connections")
	// The status label that follows each name, on both pages.
	label := func(body, name string) string {
		m := regexp.MustCompile(`(?s)` + name + `</strong>.*?class="status status-(\w+)"`).FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("no status after %s", name)
		}
		return m[1]
	}
	for name, want := range map[string]string{"ActiveOne": "active", "IdleOne": "idle", "NeverOne": "unused"} {
		if h, c := label(home, name), label(conns, name); h != want || c != want {
			t.Errorf("%s: home %q, connections %q, want %q", name, h, c, want)
		}
	}
	if !strings.Contains(home, "3 assistants: 1 active, 1 idle, 1 not used yet") {
		t.Errorf("strip does not sum to the rows: %s", home)
	}
	if strings.Count(conns, "Idle (no use for 30+ days)") != 1 {
		t.Error("connections should flag exactly the idle assistant")
	}
}
