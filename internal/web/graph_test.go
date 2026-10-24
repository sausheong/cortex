package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/sausheong/cortex"
)

func graphEnt(t *testing.T, c *cortex.Cortex, typ, name string) string {
	t.Helper()
	e := &cortex.Entity{Type: typ, Name: name, Confidence: 1, Source: "test"}
	if err := c.PutEntity(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	return e.ID
}

func graphRel(t *testing.T, c *cortex.Cortex, from, typ, to string) {
	t.Helper()
	if err := c.PutRelationship(t.Context(), &cortex.Relationship{SourceID: from, TargetID: to, Type: typ, Confidence: 1, Source: "test"}); err != nil {
		t.Fatal(err)
	}
}

func graphMention(t *testing.T, c *cortex.Cortex, content string, ents ...string) string {
	t.Helper()
	m := &cortex.Memory{Content: content, Confidence: 1, EntityIDs: ents}
	if err := c.PutMemory(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	return m.ID
}

func (e *env) graphRelCount(userID, ent string) int {
	e.t.Helper()
	var n int
	if err := e.st.DB.QueryRow(`SELECT count(*) FROM relationships WHERE user_id = $1 AND deleted_at IS NULL AND (source_id = $2 OR target_id = $2)`, userID, ent).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) graphLinkCount(userID, ent string) int {
	e.t.Helper()
	var n int
	if err := e.st.DB.QueryRow(`SELECT count(*) FROM memory_entities WHERE user_id = $1 AND entity_id = $2`, userID, ent).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// graphEnv signs alice in and returns her handle, id and owner entity.
func graphEnv(t *testing.T) (*env, *cortex.Cortex, string, string) {
	t.Helper()
	e, c, id := memEnv(t)
	owner, err := c.OwnerEntityID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return e, c, id, owner
}

func (e *env) graphJSON(path string) (int, graphJSON, http.Header) {
	e.t.Helper()
	resp, body := e.get(path)
	var g graphJSON
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal([]byte(body), &g); err != nil {
			e.t.Fatalf("bad json: %v: %s", err, body)
		}
	}
	return resp.StatusCode, g, resp.Header
}

func names(g graphJSON) []string {
	var out []string
	for _, n := range g.Nodes {
		out = append(out, n.Name)
	}
	return out
}

func TestGraphDataCentresOnOwner(t *testing.T) {
	e, c, _, owner := graphEnv(t)
	acme := graphEnt(t, c, "organization", "Acme")
	bob := graphEnt(t, c, "person", "Bobby")
	graphRel(t, c, owner, "works_at", acme)
	graphRel(t, c, owner, "knows", bob)
	graphRel(t, c, bob, "works_at", acme)
	graphEnt(t, c, "person", "Unrelated")
	code, g, h := e.graphJSON("/graph/data")
	if code != 200 || g.Center != owner || len(g.Nodes) != 3 || len(g.Edges) != 3 {
		t.Fatalf("code %d center %q nodes %v edges %d", code, g.Center, names(g), len(g.Edges))
	}
	if !g.Nodes[0].Center || g.Nodes[0].ID != owner {
		t.Fatalf("centre first: %+v", g.Nodes[0])
	}
	if h.Get("Cache-Control") != "no-store" || !strings.Contains(h.Get("Vary"), "Cookie") {
		t.Fatalf("headers %v", h)
	}
	for _, n := range g.Nodes {
		if n.Name == "Unrelated" {
			t.Fatal("unrelated entity included")
		}
		if n.Path == "" || n.Shape == "" {
			t.Fatalf("no shape on %+v", n)
		}
	}
}

func TestGraphDataIsolationAndAuth(t *testing.T) {
	e, c, _, owner := graphEnv(t)
	acme := graphEnt(t, c, "organization", "Acme")
	graphRel(t, c, owner, "works_at", acme)
	b, err := e.root.CreateUser(t.Context(), "g-bob", "bob@x.test", "Bob", true)
	if err != nil {
		t.Fatal(err)
	}
	bc := e.root.ForUser(b.ID)
	secret := graphEnt(t, bc, "person", "Bobs Secret Contact")
	bownerID, _ := bc.OwnerEntityID(t.Context())
	graphRel(t, bc, bownerID, "knows", secret)

	for _, id := range []string{secret, bownerID, "nope"} {
		resp, body := e.get("/graph/data?center=" + id)
		if resp.StatusCode != 404 || strings.Contains(body, "Secret") || strings.Contains(body, "Bob") {
			t.Fatalf("foreign/absent center %q: %d %s", id, resp.StatusCode, body)
		}
		resp, body = e.get("/graph?center=" + id)
		if resp.StatusCode != 404 || strings.Contains(body, "Bobs Secret") {
			t.Fatalf("page, foreign center %q: %d", id, resp.StatusCode)
		}
		// The page still shows the viewer's own graph.
		if !strings.Contains(body, "Acme") {
			t.Fatalf("page should fall back to own graph")
		}
	}
	// Alice's own data never contains Bob's entities.
	_, g, _ := e.graphJSON("/graph/data")
	for _, n := range g.Nodes {
		if strings.Contains(n.Name, "Bob") {
			t.Fatalf("leak: %v", names(g))
		}
	}
	// Signed out: 401, not a redirect, and nothing in the body.
	e.newClient()
	resp, body := e.get("/graph/data")
	if resp.StatusCode != 401 || strings.Contains(body, "Acme") {
		t.Fatalf("signed out: %d %s", resp.StatusCode, body)
	}
}

func TestGraphDataNodeCapAndRanking(t *testing.T) {
	e, c, _, owner := graphEnv(t)
	var strong string
	for i := 0; i < 40; i++ {
		id := graphEnt(t, c, "person", fmt.Sprintf("Person %02d", i))
		graphRel(t, c, owner, "knows", id)
		if i == 39 {
			strong = id
		}
	}
	// One more relationship with the centre makes the last one rank first.
	graphRel(t, c, strong, "colleague_of", owner)
	_, g, _ := e.graphJSON("/graph/data?limit=30")
	if len(g.Nodes) != 30 || !g.Truncated || g.Limit != 30 {
		t.Fatalf("nodes %d truncated %v limit %d", len(g.Nodes), g.Truncated, g.Limit)
	}
	if g.Nodes[1].ID != strong || g.Nodes[1].Rels != 2 {
		t.Fatalf("strongest neighbour should rank first: %+v", g.Nodes[1])
	}
	_, g, _ = e.graphJSON("/graph/data?limit=120")
	if len(g.Nodes) != 41 || g.Truncated {
		t.Fatalf("limit 120: %d nodes truncated %v", len(g.Nodes), g.Truncated)
	}
	// Any other limit falls back to the default of 60.
	_, g, _ = e.graphJSON("/graph/data?limit=100000")
	if g.Limit != 60 || len(g.Nodes) != 41 {
		t.Fatalf("limit fallback: %d %d", g.Limit, len(g.Nodes))
	}
	for _, n := range g.Nodes {
		if n.Center && n.ID != owner {
			t.Fatal("wrong centre")
		}
	}
}

func TestGraphDataFilters(t *testing.T) {
	e, c, id, owner := graphEnv(t)
	acme := graphEnt(t, c, "organization", "Acme")
	bob := graphEnt(t, c, "person", "Bobby")
	proj := graphEnt(t, c, "project", "Apollo")
	for _, x := range []string{acme, bob, proj} {
		graphRel(t, c, owner, "related_to", x)
	}
	recent := graphMention(t, c, "Met Bobby today", bob)
	old := graphMention(t, c, "Apollo kickoff long ago", proj)
	if _, err := e.st.DB.Exec(`UPDATE memories SET created_at = now() - interval '100 days' WHERE id = $1 AND user_id = $2`, old, id); err != nil {
		t.Fatal(err)
	}
	_ = recent

	_, g, _ := e.graphJSON("/graph/data?types=person")
	if got := names(g); len(got) != 2 { // centre (a person) and Bobby
		t.Fatalf("types=person: %v", got)
	}
	_, g, _ = e.graphJSON("/graph/data?types=organization,project")
	if len(g.Nodes) != 3 { // centre is always kept
		t.Fatalf("types=org,project: %v", names(g))
	}
	_, g, _ = e.graphJSON("/graph/data?days=30")
	if got := names(g); len(got) != 2 || got[1] != "Bobby" {
		t.Fatalf("days=30: %v", got)
	}
	_, g, _ = e.graphJSON("/graph/data?days=365")
	if len(g.Nodes) != 3 { // Bobby and Apollo are mentioned, Acme is not
		t.Fatalf("days=365: %v", names(g))
	}
	// A soft-deleted mention no longer counts as a recent mention.
	if _, err := e.st.DB.Exec(`UPDATE memories SET deleted_at = now() WHERE id = $1`, recent); err != nil {
		t.Fatal(err)
	}
	_, g, _ = e.graphJSON("/graph/data?days=30")
	if len(g.Nodes) != 1 {
		t.Fatalf("deleted mention counted: %v", names(g))
	}
}

func TestGraphPageRendersListWithoutScript(t *testing.T) {
	e, c, _, owner := graphEnv(t)
	acme := graphEnt(t, c, "organization", "Acme")
	graphRel(t, c, owner, "works_at", acme)
	graphMention(t, c, "Started at Acme in March", acme)
	resp, body := e.get("/graph?center=" + acme)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	for _, want := range []string{
		`id="node-list"`, `href="/graph?center=` + owner, "Alice", // the neighbour list
		"works at",                 // readable relationship label
		"Started at Acme in March", // memories that mention it
		"/memories?entity=" + acme, // linked to Memories filtered to the entity
		`data-src="/graph/data?center=` + acme,
		`>Graph</a>`,             // nav item
		`graph-layout is-sparse`, // two nodes: list leads, small map
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// Relationship direction reads as a sentence: Alice works at Acme.
	if !regexp.MustCompile(`(?s)<a href="[^"]+">Alice</a> <span class="rel-label">works at</span> <span class="rel-self">Acme</span>`).MatchString(body) {
		t.Errorf("direction sentence missing: %s", body)
	}
	// The default page centres on the owner.
	_, body = e.get("/graph")
	if !strings.Contains(body, `data-src="/graph/data?center=`+owner) || !strings.Contains(body, "This is you") {
		t.Errorf("default centre is not the owner")
	}
	// Nav order: Memories, Graph, Connections.
	i, j, k := strings.Index(body, ">Memories</a>"), strings.Index(body, ">Graph</a>"), strings.Index(body, ">Connections</a>")
	if !(i < j && j < k) {
		t.Errorf("nav order %d %d %d", i, j, k)
	}
}

func TestGraphSearchIsScopedAndLiteral(t *testing.T) {
	e, c, _, _ := graphEnv(t)
	graphEnt(t, c, "person", "Grace Hopper")
	graphEnt(t, c, "person", "100% Real")
	b, _ := e.root.CreateUser(t.Context(), "g-bob", "bob@x.test", "Bob", true)
	graphEnt(t, e.root.ForUser(b.ID), "person", "Gracefully Foreign")
	_, body := e.get("/graph?q=grace")
	if !strings.Contains(body, "Grace Hopper") || strings.Contains(body, "Foreign") {
		t.Fatalf("search: %s", body)
	}
	_, body = e.get("/graph?q=" + url.QueryEscape("%"))
	if !strings.Contains(body, "100% Real") || strings.Contains(body, "Grace Hopper") {
		t.Fatalf("percent must be literal: %s", body)
	}
	_, body = e.get("/graph?q=zzzz")
	if !strings.Contains(body, "Nothing matches") {
		t.Fatalf("empty search: %s", body)
	}
}

func TestGraphRenameAndCollisionOffersMerge(t *testing.T) {
	e, c, _, owner := graphEnv(t)
	a := graphEnt(t, c, "person", "Bobby")
	b := graphEnt(t, c, "person", "Robert")
	graphEnt(t, c, "organization", "Robby") // same name, other type: no collision
	csrf := e.csrf()

	resp, body := e.post("/graph/rename", url.Values{"csrf": {csrf}, "id": {a}, "name": {"  Rob  "}})
	if resp.StatusCode != 303 {
		t.Fatalf("rename: %d %s", resp.StatusCode, body)
	}
	if ent, _ := c.GetEntity(t.Context(), a); ent.Name != "Rob" {
		t.Fatalf("name %q", ent.Name)
	}
	if resp, _ := e.post("/graph/rename", url.Values{"csrf": {csrf}, "id": {a}, "name": {"Robby"}}); resp.StatusCode != 303 {
		t.Fatalf("same name other type must be allowed: %d", resp.StatusCode)
	}

	// Collision: the name belongs to another person -> offer Merge, rename nothing.
	resp, body = e.post("/graph/rename", url.Values{"csrf": {csrf}, "id": {a}, "name": {"Robert"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, "Merge instead?") || !strings.Contains(body, `name="keep" value="`+b+`"`) || !strings.Contains(body, `name="drop" value="`+a+`"`) {
		t.Fatalf("collision: %d %s", resp.StatusCode, body)
	}
	if ent, _ := c.GetEntity(t.Context(), a); ent.Name != "Robby" {
		t.Fatalf("collision must not rename: %q", ent.Name)
	}

	// Validation.
	for _, bad := range []string{"", "   ", strings.Repeat("x", 201), "two\nlines", "Robby"} {
		resp, body := e.post("/graph/rename", url.Values{"csrf": {csrf}, "id": {a}, "name": {bad}})
		if resp.StatusCode != 400 || !strings.Contains(body, `aria-invalid="true"`) {
			t.Fatalf("bad name %q: %d", bad, resp.StatusCode)
		}
	}

	// The owner entity can be renamed, but a collision offers no merge.
	graphEnt(t, c, "person", "Taken")
	resp, body = e.post("/graph/rename", url.Values{"csrf": {csrf}, "id": {owner}, "name": {"Taken"}})
	if resp.StatusCode != 409 || strings.Contains(body, "Merge instead?") {
		t.Fatalf("owner collision: %d", resp.StatusCode)
	}
	if resp, _ := e.post("/graph/rename", url.Values{"csrf": {csrf}, "id": {owner}, "name": {"Alice A"}}); resp.StatusCode != 303 {
		t.Fatalf("owner rename: %d", resp.StatusCode)
	}
}

func TestGraphOwnerEntityIsProtected(t *testing.T) {
	e, c, uid, owner := graphEnv(t)
	other := graphEnt(t, c, "person", "Twin")
	graphRel(t, c, owner, "knows", other)
	csrf := e.csrf()

	// Deleting: refused at the first step and at the second.
	for _, f := range []url.Values{
		{"csrf": {csrf}, "id": {owner}},
		{"csrf": {csrf}, "id": {owner}, "confirm_count": {"1"}},
	} {
		resp, body := e.post("/graph/delete", f)
		if resp.StatusCode != 403 || !strings.Contains(body, "cannot be deleted") {
			t.Fatalf("delete owner: %d", resp.StatusCode)
		}
	}
	// Merging it away: refused, with or without confirm.
	for _, f := range []url.Values{
		{"csrf": {csrf}, "keep": {other}, "drop": {owner}},
		{"csrf": {csrf}, "keep": {other}, "drop": {owner}, "confirm_count": {"1"}},
	} {
		resp, _ := e.post("/graph/merge", f)
		if resp.StatusCode != 403 {
			t.Fatalf("merge owner away: %d", resp.StatusCode)
		}
	}
	if _, err := c.GetEntity(t.Context(), owner); err != nil {
		t.Fatalf("owner entity damaged: %v", err)
	}
	if e.graphRelCount(uid, owner) != 1 {
		t.Fatal("owner relationships changed")
	}
	// The panel does not offer either action.
	_, body := e.get("/graph?center=" + owner)
	if strings.Contains(body, `action="/graph/delete"`) || !strings.Contains(body, "cannot be deleted") || !strings.Contains(body, "cannot be merged into another one") {
		t.Fatalf("owner panel offers destructive actions")
	}
	// It can be the merge target.
	resp, body := e.post("/graph/merge", url.Values{"csrf": {csrf}, "keep": {owner}, "drop": {other}})
	if resp.StatusCode != 200 || !strings.Contains(body, "Merge Twin into Alice?") {
		t.Fatalf("owner as target: %d", resp.StatusCode)
	}
}

func TestGraphDeleteConfirmAndUndo(t *testing.T) {
	e, c, uid, owner := graphEnv(t)
	acme := graphEnt(t, c, "organization", "Acme")
	bob := graphEnt(t, c, "person", "Bobby")
	graphRel(t, c, owner, "works_at", acme)
	graphRel(t, c, bob, "works_at", acme)
	graphMention(t, c, "Acme is hiring", acme)
	csrf := e.csrf()

	// Step 1 only previews: consequence, count, 30-day recovery; nothing changes.
	resp, body := e.post("/graph/delete", url.Values{"csrf": {csrf}, "id": {acme}})
	if resp.StatusCode != 200 || !strings.Contains(body, "Delete Acme?") || !strings.Contains(body, "2 relationships") ||
		!strings.Contains(body, "30 days") || !strings.Contains(body, "Memories that mention it are kept") {
		t.Fatalf("preview: %d %s", resp.StatusCode, body)
	}
	if _, err := c.GetEntity(t.Context(), acme); err != nil {
		t.Fatal("preview deleted the entity")
	}
	// A stale count is refused and shown again.
	resp, body = e.post("/graph/delete", url.Values{"csrf": {csrf}, "id": {acme}, "confirm_count": {"5"}})
	if resp.StatusCode != 409 || !strings.Contains(body, "changed from 5 to 2") {
		t.Fatalf("stale: %d", resp.StatusCode)
	}
	if _, err := c.GetEntity(t.Context(), acme); err != nil {
		t.Fatal("stale confirm deleted")
	}
	// No CSRF: nothing happens.
	if resp, _ := e.post("/graph/delete", url.Values{"id": {acme}, "confirm_count": {"2"}}); resp.StatusCode != 403 {
		t.Fatalf("csrf: %d", resp.StatusCode)
	}

	resp, _ = e.post("/graph/delete", url.Values{"csrf": {csrf}, "id": {acme}, "confirm_count": {"2"}})
	loc := resp.Header.Get("Location")
	if resp.StatusCode != 303 || !strings.HasPrefix(loc, "/graph?batch=") {
		t.Fatalf("delete: %d %s", resp.StatusCode, loc)
	}
	if _, err := c.GetEntity(t.Context(), acme); err == nil {
		t.Fatal("entity still live")
	}
	if e.graphRelCount(uid, owner) != 0 {
		t.Fatal("relationship should be gone from the graph")
	}
	_, body = e.get(loc)
	if !strings.Contains(body, "Deleted this item and 2 relationships") || !strings.Contains(body, "30 days") || !strings.Contains(body, "Undo delete") {
		t.Fatalf("undo notice: %s", body)
	}
	// The mention survives.
	if _, n, _ := c.EntityMentions(t.Context(), acme, 5); n != 0 {
		// the entity is gone, so the filter finds nothing; the memory itself is live
		t.Logf("mentions via deleted entity: %d", n)
	}
	if liveCount(t, c) != 1 {
		t.Fatal("memory should be kept")
	}

	batch := regexp.MustCompile(`/graph/restore/([A-Za-z0-9]+)`).FindStringSubmatch(body)[1]
	resp, _ = e.post("/graph/restore/"+batch, url.Values{"csrf": {csrf}, "entity": {acme}})
	if resp.StatusCode != 303 || !strings.Contains(resp.Header.Get("Location"), "center="+acme) {
		t.Fatalf("restore: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if _, err := c.GetEntity(t.Context(), acme); err != nil {
		t.Fatal("not restored")
	}
	if e.graphRelCount(uid, acme) != 2 {
		t.Fatalf("relationships after undo = %d", e.graphRelCount(uid, acme))
	}
	_, body = e.get(resp.Header.Get("Location"))
	if !strings.Contains(body, "Restored this item with") {
		t.Fatal("no restored notice")
	}
}

func TestGraphMergeConfirmAndUndoCounts(t *testing.T) {
	e, c, uid, owner := graphEnv(t)
	keep := graphEnt(t, c, "person", "Robert")
	drop := graphEnt(t, c, "person", "Bobby")
	acme := graphEnt(t, c, "organization", "Acme")
	org := graphEnt(t, c, "organization", "Zed")
	graphRel(t, c, drop, "works_at", acme)
	graphRel(t, c, owner, "knows", drop)
	graphRel(t, c, keep, "works_at", acme) // duplicate of drop's after the move
	graphMention(t, c, "Bobby likes tea", drop)
	graphMention(t, c, "Bobby and Robert met", drop, keep)
	csrf := e.csrf()
	beforeKeepRels, beforeDropRels := e.graphRelCount(uid, keep), e.graphRelCount(uid, drop)
	beforeKeepLinks, beforeDropLinks := e.graphLinkCount(uid, keep), e.graphLinkCount(uid, drop)

	// Different types cannot be merged.
	if resp, _ := e.post("/graph/merge", url.Values{"csrf": {csrf}, "keep": {org}, "drop": {drop}}); resp.StatusCode != 400 {
		t.Fatalf("cross-type: %d", resp.StatusCode)
	}
	// Preview states what moves; nothing changes yet.
	resp, body := e.post("/graph/merge", url.Values{"csrf": {csrf}, "keep": {keep}, "drop": {drop}})
	if resp.StatusCode != 200 || !strings.Contains(body, "Merge Bobby into Robert?") || !strings.Contains(body, "1 relationship") ||
		!strings.Contains(body, "30 days") || !strings.Contains(body, "memory link") {
		t.Fatalf("preview: %d %s", resp.StatusCode, body)
	}
	m := regexp.MustCompile(`name="confirm_count" value="(\d+)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no count")
	}
	if _, err := c.GetEntity(t.Context(), drop); err != nil {
		t.Fatal("preview merged")
	}
	if resp, _ := e.post("/graph/merge", url.Values{"csrf": {csrf}, "keep": {keep}, "drop": {drop}, "confirm_count": {"99"}}); resp.StatusCode != 409 {
		t.Fatalf("stale: %d", resp.StatusCode)
	}

	resp, _ = e.post("/graph/merge", url.Values{"csrf": {csrf}, "keep": {keep}, "drop": {drop}, "confirm_count": {m[1]}})
	loc := resp.Header.Get("Location")
	if resp.StatusCode != 303 || !strings.Contains(loc, "center="+keep) || !strings.Contains(loc, "batch=") {
		t.Fatalf("merge: %d %s", resp.StatusCode, loc)
	}
	if _, err := c.GetEntity(t.Context(), drop); err == nil {
		t.Fatal("dropped entity still live")
	}
	if got := e.graphLinkCount(uid, keep); got != 2 { // both memories now point at keep once
		t.Fatalf("keep links after merge = %d", got)
	}
	_, body = e.get(loc)
	if !strings.Contains(body, "Merged into Robert") || !strings.Contains(body, "Undo merge") {
		t.Fatalf("notice: %s", body)
	}

	batch := regexp.MustCompile(`/graph/restore/([A-Za-z0-9]+)`).FindStringSubmatch(body)[1]
	resp, _ = e.post("/graph/restore/"+batch, url.Values{"csrf": {csrf}, "entity": {drop}})
	if resp.StatusCode != 303 || !strings.Contains(resp.Header.Get("Location"), "restored=merge") {
		t.Fatalf("undo: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if _, err := c.GetEntity(t.Context(), drop); err != nil {
		t.Fatal("drop not restored")
	}
	if got := [4]int{e.graphRelCount(uid, keep), e.graphRelCount(uid, drop), e.graphLinkCount(uid, keep), e.graphLinkCount(uid, drop)}; got != [4]int{beforeKeepRels, beforeDropRels, beforeKeepLinks, beforeDropLinks} {
		t.Fatalf("counts after undo %v, want %v", got, [4]int{beforeKeepRels, beforeDropRels, beforeKeepLinks, beforeDropLinks})
	}
	// A batch that is not a merge or entity delete cannot be restored here.
	if resp, _ := e.post("/graph/restore/nope", url.Values{"csrf": {csrf}}); resp.StatusCode != 404 {
		t.Fatalf("unknown batch: %d", resp.StatusCode)
	}
}

func TestGraphMutationsTreatForeignIDsAsAbsent(t *testing.T) {
	e, c, _, _ := graphEnv(t)
	mine := graphEnt(t, c, "person", "Mine")
	b, _ := e.root.CreateUser(t.Context(), "g-bob", "bob@x.test", "Bob", true)
	bc := e.root.ForUser(b.ID)
	theirs := graphEnt(t, bc, "person", "Theirs")
	csrf := e.csrf()

	for name, call := range map[string]func() *http.Response{
		"rename": func() *http.Response {
			r, _ := e.post("/graph/rename", url.Values{"csrf": {csrf}, "id": {theirs}, "name": {"Pwned"}})
			return r
		},
		"delete": func() *http.Response {
			r, _ := e.post("/graph/delete", url.Values{"csrf": {csrf}, "id": {theirs}, "confirm_count": {"0"}})
			return r
		},
		"merge drop": func() *http.Response {
			r, _ := e.post("/graph/merge", url.Values{"csrf": {csrf}, "keep": {mine}, "drop": {theirs}, "confirm_count": {"0"}})
			return r
		},
		"merge keep": func() *http.Response {
			r, _ := e.post("/graph/merge", url.Values{"csrf": {csrf}, "keep": {theirs}, "drop": {mine}, "confirm_count": {"0"}})
			return r
		},
	} {
		if resp := call(); resp.StatusCode != 404 {
			t.Errorf("%s with a foreign id: %d, want 404", name, resp.StatusCode)
		}
	}
	if ent, err := bc.GetEntity(t.Context(), theirs); err != nil || ent.Name != "Theirs" {
		t.Fatalf("foreign entity touched: %v %v", ent, err)
	}
	if _, err := c.GetEntity(t.Context(), mine); err != nil {
		t.Fatal("own entity touched")
	}
}

func TestGraphMergeSearchOffersSameTypeOnly(t *testing.T) {
	e, c, _, _ := graphEnv(t)
	a := graphEnt(t, c, "person", "Ann Lee")
	graphEnt(t, c, "person", "Anna Lee")
	graphEnt(t, c, "organization", "Annex")
	_, body := e.get("/graph?center=" + a + "&mergeq=ann")
	if !strings.Contains(body, "Anna Lee") || strings.Contains(body, "Annex") {
		t.Fatalf("merge candidates: %s", body)
	}
	if strings.Count(body, `name="keep"`) != 1 { // only Anna: not itself, not another type
		t.Fatalf("candidates wrong: %d", strings.Count(body, `name="keep"`))
	}
}

func TestMemoriesEntityFilter(t *testing.T) {
	e, c, _, _ := graphEnv(t)
	acme := graphEnt(t, c, "organization", "Acme")
	graphMention(t, c, "about acme", acme)
	putMem(t, c, "about nothing")
	_, body := e.get("/memories?entity=" + acme)
	if !strings.Contains(body, "about acme") || strings.Contains(body, "about nothing") || !strings.Contains(body, "mention <strong>Acme</strong>") {
		t.Fatalf("entity filter: %s", body)
	}
	b, _ := e.root.CreateUser(t.Context(), "g-bob", "bob@x.test", "Bob", true)
	theirs := graphEnt(t, e.root.ForUser(b.ID), "person", "Theirs")
	graphMention(t, e.root.ForUser(b.ID), "bobs private note", theirs)
	_, body = e.get("/memories?entity=" + theirs)
	if strings.Contains(body, "bobs private note") || strings.Contains(body, "Theirs") || strings.Contains(body, "about acme") {
		t.Fatalf("foreign entity filter leaked or matched: %s", body)
	}
}
