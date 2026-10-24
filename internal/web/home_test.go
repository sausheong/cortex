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
	// Home is first in the nav.
	if strings.Index(body, ">Home<") > strings.Index(body, ">Memories<") {
		t.Error("Home must come before Memories")
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
	_, conn, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "agent")
	e.write(ua.ID, conn.ID, "likes tea")
	e.st.DB.ExecContext(t.Context(), `UPDATE connections SET last_used = $2 WHERE id = $1`, conn.ID, time.Now())
	_, body = e.get("/")
	if strings.Contains(body, "Get started") {
		t.Fatal("checklist still shown when setup is complete")
	}
	if !strings.Contains(body, "You are set up.") {
		t.Error("complete setup should show the quiet set-up line")
	}
	for _, want := range []string{"About you", "Nightly maintenance", `class="home-strip"`, "1 assistant active"} {
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
	_, chat, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "ChatGPT")
	_, other, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "Claude")
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
	_, chat, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "ChatGPT")
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
	_, conn, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "Gone")
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
	_, conn, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "Agent")
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
	_, fresh, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "FreshOne")
	_, idle, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "IdleOne")
	_, _, _ = e.st.CreateAgentToken(t.Context(), ua.ID, "NeverOne")
	_, gone, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "GoneOne")
	if err := e.st.RevokeConnection(t.Context(), ua.ID, gone.ID); err != nil {
		t.Fatal(err)
	}
	db := e.st.DB
	db.ExecContext(t.Context(), `UPDATE connections SET last_used = $2 WHERE id = $1`, fresh.ID, time.Now().Add(-time.Hour))
	db.ExecContext(t.Context(), `UPDATE connections SET last_used = $2 WHERE id = $1`, idle.ID, time.Now().Add(-45*24*time.Hour))
	e.write(ua.ID, fresh.ID, "a")
	e.write(ua.ID, fresh.ID, "b")
	_, body := e.get("/")
	for _, want := range []string{"FreshOne", "IdleOne", "NeverOne", "Not used yet", "Idle for over 30 days", "2 written in the last 7 days", "0 written in the last 7 days"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(body, "GoneOne") {
		t.Error("revoked connection listed")
	}
	if strings.Count(body, "Idle for over 30 days") != 1 {
		t.Error("only the idle connection is flagged")
	}
}

func TestHomeHealthRows(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	ua := e.userBySub("g-alice")
	e.setProvider(ua.ID)
	_, conn, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "agent")
	e.write(ua.ID, conn.ID, "seed")
	_, body := e.get("/")
	for _, want := range []string{"Runs overnight once", "Configured with gpt-test", "Not measured yet. Updated overnight."} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
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
	if !strings.Contains(body, "Ran 3 hours ago. Everything went through.") || strings.Contains(body, "Measured overnight") {
		t.Errorf("ok state: %s", body)
	}
	set("maintenance completed; 2 item(s) skipped after provider errors")
	_, body = e.get("/")
	if !strings.Contains(body, "2 items were skipped") {
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
	e.st.CreateAgentToken(t.Context(), ua.ID, "agent")
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
	_, ac, _ := e.st.CreateAgentToken(t.Context(), ua.ID, "AliceBot")
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
