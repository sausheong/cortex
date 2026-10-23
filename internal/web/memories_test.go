package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sausheong/cortex"
)

// memEnv signs alice in and returns her handle.
func memEnv(t *testing.T) (*env, *cortex.Cortex, string) {
	t.Helper()
	e := newEnv(t)
	e.signIn(alice(), "")
	id := e.userBySub("g-alice").ID
	return e, e.root.ForUser(id), id
}

func putMem(t *testing.T, c *cortex.Cortex, content string) string {
	t.Helper()
	m := &cortex.Memory{Content: content, Confidence: 1}
	if err := c.PutMemory(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	return m.ID
}

func liveCount(t *testing.T, c *cortex.Cortex) int {
	t.Helper()
	_, n, err := c.ListMemories(t.Context(), cortex.MemoryListFilter{}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMemoriesPageFiltersByConnection(t *testing.T) {
	e, c, id := memEnv(t)
	conn, err := e.st.CreateConnection(t.Context(), id, "agent", "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	putMem(t, c.WithConnection(conn.ID), "written via laptop")
	putMem(t, c, "written by hand")
	_, body := e.get("/memories?by=" + conn.ID)
	if !strings.Contains(body, "written via laptop") || strings.Contains(body, "written by hand") {
		t.Fatalf("filter wrong: %s", body)
	}
	_, body = e.get("/memories?q=hand")
	if strings.Contains(body, "written via laptop") || !strings.Contains(body, "written by hand") {
		t.Fatalf("q filter wrong: %s", body)
	}
}

func TestBulkDeleteOlderThan(t *testing.T) {
	e, c, id := memEnv(t)
	old := putMem(t, c, "ancient")
	putMem(t, c, "fresh")
	if _, err := e.st.DB.Exec(`UPDATE memories SET created_at = now() - interval '40 days' WHERE id = $1 AND user_id = $2`, old, id); err != nil {
		t.Fatal(err)
	}
	e.now = time.Now().UTC()
	csrf := e.csrf()
	// Step 1 only previews: nothing is deleted, the exact count is shown.
	resp, body := e.post("/memories/delete", url.Values{"csrf": {csrf}, "scope": {"matching"}, "older_than": {"30"}})
	if resp.StatusCode != http.StatusOK || liveCount(t, c) != 2 || !strings.Contains(body, "Delete 1 memory?") || !strings.Contains(body, "30 days") {
		t.Fatalf("preview: status %d live %d: %s", resp.StatusCode, liveCount(t, c), body)
	}
	resp, _ = e.post("/memories/delete", url.Values{"csrf": {csrf}, "scope": {"matching"}, "older_than": {"30"}, "confirm_count": {"1"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status %d", resp.StatusCode)
	}
	_, body = e.get("/memories")
	if strings.Contains(body, "ancient") || !strings.Contains(body, "fresh") {
		t.Fatalf("wrong rows: %s", body)
	}
}

func TestClearAllNeedsCountAndTypedPhrase(t *testing.T) {
	e, c, _ := memEnv(t)
	putMem(t, c, "one")
	putMem(t, c, "two")
	csrf := e.csrf()
	// Preview states the count and the phrase to type; nothing is deleted.
	resp, body := e.post("/memories/delete", url.Values{"csrf": {csrf}, "scope": {"all"}, "all": {"1"}})
	if resp.StatusCode != http.StatusOK || liveCount(t, c) != 2 ||
		!strings.Contains(body, "Delete all 2 memories?") || !strings.Contains(body, clearAllPhrase) || !strings.Contains(body, "30 days") {
		t.Fatalf("preview: %d: %s", resp.StatusCode, body)
	}
	if strings.Contains(body, "delete my brain") {
		t.Fatal("joke phrase still present")
	}
	// Missing or wrong count, or missing or wrong phrase: refused.
	for _, f := range []url.Values{
		{"confirm": {clearAllPhrase}},                                     // no count
		{"confirm_count": {"2"}},                                          // no phrase
		{"confirm_count": {"2"}, "confirm": {"delete my brain"}},          // wrong phrase
		{"confirm_count": {"2"}, "confirm": {"delete everything please"}}, // near miss
		{"confirm_count": {"1"}, "confirm": {clearAllPhrase}},             // stale count
		{"confirm_count": {"abc"}, "confirm": {clearAllPhrase}},           // malformed
	} {
		f.Set("csrf", csrf)
		f.Set("all", "1")
		f.Set("scope", "all")
		resp, _ := e.post("/memories/delete", f)
		if resp.StatusCode == http.StatusSeeOther || liveCount(t, c) != 2 {
			t.Fatalf("%v: status %d live %d", f, resp.StatusCode, liveCount(t, c))
		}
	}
	resp, _ = e.post("/memories/delete", url.Values{"csrf": {csrf}, "scope": {"all"}, "all": {"1"}, "confirm_count": {"2"}, "confirm": {"  Delete Everything "}})
	if resp.StatusCode != http.StatusSeeOther || liveCount(t, c) != 0 {
		t.Fatalf("status %d live %d", resp.StatusCode, liveCount(t, c))
	}
}

func TestStaleCountShowsNewCountAndDeletesNothing(t *testing.T) {
	e, c, _ := memEnv(t)
	a, b := putMem(t, c, "keep-a"), putMem(t, c, "keep-b")
	csrf := e.csrf()
	_ = a
	// The user confirmed 5 but only 2 match now.
	resp, body := e.post("/memories/delete", url.Values{"csrf": {csrf}, "scope": {"matching"}, "match": {"keep"}, "confirm_count": {"5"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, "changed from 5 to 2") || liveCount(t, c) != 2 {
		t.Fatalf("status %d live %d: %s", resp.StatusCode, liveCount(t, c), body)
	}
	_ = b
}

func TestDeleteSelectedShowsUndoNoticeAndUndoRestores(t *testing.T) {
	e, c, _ := memEnv(t)
	id1 := putMem(t, c, "first")
	id2 := putMem(t, c, "second")
	putMem(t, c, "third")
	csrf := e.csrf()
	// Preview with the exact count; nothing deleted yet.
	resp, body := e.post("/memories/delete", url.Values{"csrf": {csrf}, "scope": {"selected"}, "ids": {id1, id2}})
	if resp.StatusCode != 200 || !strings.Contains(body, "Delete 2 memories?") || !strings.Contains(body, "You can restore them for 30 days.") || liveCount(t, c) != 3 {
		t.Fatalf("preview: %d %s", resp.StatusCode, body)
	}
	resp, _ = e.post("/memories/delete", url.Values{"csrf": {csrf}, "scope": {"selected"}, "ids": {id1, id2}, "confirm_count": {"2"}})
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/memories?batch=") || liveCount(t, c) != 1 {
		t.Fatalf("status %d loc %q live %d", resp.StatusCode, loc, liveCount(t, c))
	}
	// Persistent notice with Undo, built from the stored batch.
	_, body = e.get(loc)
	m := regexpBatch.FindStringSubmatch(body)
	if !strings.Contains(body, "Deleted 2 memories.") || !strings.Contains(body, "Undo delete") || m == nil {
		t.Fatalf("no undo notice: %s", body)
	}
	// A forged batch id shows no notice.
	_, forged := e.get("/memories?batch=nope")
	if strings.Contains(forged, "Undo delete") {
		t.Fatal("forged batch shows undo")
	}
	resp, _ = e.post("/memories/restore/"+m[1], url.Values{"csrf": {csrf}})
	if resp.StatusCode != http.StatusSeeOther || liveCount(t, c) != 3 || resp.Header.Get("Location") != "/memories?restored=2" {
		t.Fatalf("undo: status %d live %d loc %q", resp.StatusCode, liveCount(t, c), resp.Header.Get("Location"))
	}
	_, body = e.get("/memories?restored=2")
	if !strings.Contains(body, "Restored 2 memories.") {
		t.Fatalf("restored notice: %s", body)
	}
}

func TestDeleteErrorsTieToFieldsWithNextStep(t *testing.T) {
	e, c, _ := memEnv(t)
	putMem(t, c, "one")
	csrf := e.csrf()
	for _, tc := range []struct {
		form    url.Values
		wantErr string
		field   string
	}{
		{url.Values{"scope": {"selected"}}, "Tick at least one memory", `id="err-ids"`},
		{url.Values{"scope": {"matching"}, "older_than": {"abc"}}, "whole number of days", `aria-invalid="true"`},
		{url.Values{"scope": {"matching"}}, "Choose who wrote it", `id="err-match"`},
		{url.Values{"scope": {"matching"}, "match": {"zzz"}}, "No memories match", `id="err-match"`},
	} {
		tc.form.Set("csrf", csrf)
		resp, body := e.post("/memories/delete", tc.form)
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, tc.wantErr) || !strings.Contains(body, tc.field) || liveCount(t, c) != 1 {
			t.Fatalf("%v: %d %s", tc.form, resp.StatusCode, body)
		}
	}
}

func TestRestoreBatchFromPage(t *testing.T) {
	e, c, _ := memEnv(t)
	id1 := putMem(t, c, "keep me")
	csrf := e.csrf()
	e.post("/memories/delete", url.Values{"csrf": {csrf}, "scope": {"selected"}, "ids": {id1}, "confirm_count": {"1"}})
	if liveCount(t, c) != 0 {
		t.Fatal("not deleted")
	}
	_, body := e.get("/memories/deleted")
	m := regexpBatch.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no restore form: %s", body)
	}
	resp, _ := e.post("/memories/restore/"+m[1], url.Values{"csrf": {csrf}})
	if resp.StatusCode != http.StatusSeeOther || liveCount(t, c) != 1 {
		t.Fatalf("status %d live %d", resp.StatusCode, liveCount(t, c))
	}
}

func TestRestoreForeignBatchIs404(t *testing.T) {
	e, c, _ := memEnv(t)
	b, err := e.root.CreateUser(t.Context(), "g-b", "b@x.test", "B", true)
	if err != nil {
		t.Fatal(err)
	}
	bc := e.root.ForUser(b.ID)
	bm := putMem(t, bc, "b secret")
	batch, n, err := bc.SoftDeleteMemories(t.Context(), cortex.DeleteFilter{IDs: []string{bm}}, "t")
	if err != nil || n != 1 {
		t.Fatal(err, n)
	}
	csrf := e.csrf()
	r1, b1 := e.post("/memories/restore/"+batch, url.Values{"csrf": {csrf}})
	r2, b2 := e.post("/memories/restore/does-not-exist", url.Values{"csrf": {csrf}})
	if r1.StatusCode != 404 || r2.StatusCode != 404 || b1 != b2 {
		t.Fatalf("foreign %d absent %d", r1.StatusCode, r2.StatusCode)
	}
	if liveCount(t, bc) != 0 {
		t.Fatal("b's rows were restored")
	}
	_ = c
}

func TestMemoryDeleteForeignIDsIgnored(t *testing.T) {
	e, _, _ := memEnv(t)
	b, err := e.root.CreateUser(t.Context(), "g-b", "b@x.test", "B", true)
	if err != nil {
		t.Fatal(err)
	}
	bc := e.root.ForUser(b.ID)
	bm := putMem(t, bc, "b secret")
	for _, f := range []url.Values{{}, {"confirm_count": {"1"}}, {"confirm_count": {"0"}}} {
		f.Set("csrf", e.csrf())
		f.Set("ids", bm)
		if r, _ := e.post("/memories/delete", f); r.StatusCode == http.StatusSeeOther {
			t.Fatalf("foreign ids accepted: %v", f)
		}
	}
	if liveCount(t, bc) != 1 {
		t.Fatal("b's memory was deleted by a")
	}
}

func TestUsageCountsOwnRowsOnly(t *testing.T) {
	e, c, id := memEnv(t)
	b, err := e.root.CreateUser(t.Context(), "g-b", "b@x.test", "B", true)
	if err != nil {
		t.Fatal(err)
	}
	bc := e.root.ForUser(b.ID)
	putMem(t, bc, "b one")
	putMem(t, bc, "b two")
	m1 := putMem(t, c, "a one")
	e.insertEmbedding(id)
	u, err := c.ComputeUsage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if u.Memories != 1 || u.Bytes <= 0 {
		t.Fatalf("usage %+v", u)
	}
	before := u.Bytes
	if _, _, err := c.SoftDeleteMemories(t.Context(), cortex.DeleteFilter{IDs: []string{m1}}, "t"); err != nil {
		t.Fatal(err)
	}
	if u, _ = c.ComputeUsage(t.Context()); u.Memories != 0 || u.Deleted != 1 {
		t.Fatalf("after delete %+v", u)
	}
	// Purge everything: the memory, and the embedding that referenced it.
	if _, err := e.st.DB.Exec(`UPDATE embeddings SET ref_id = $1 WHERE user_id = $2`, m1, id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.DB.Exec(`UPDATE memories SET deleted_at = now() - interval '40 days' WHERE id = $1`, m1); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PurgeDeleted(t.Context(), 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	u, _ = c.ComputeUsage(t.Context())
	if u.Memories != 0 || u.Deleted != 0 || u.Bytes >= before {
		t.Fatalf("after purge %+v (before %d)", u, before)
	}
	if ub, _ := bc.ComputeUsage(t.Context()); ub.Memories != 2 {
		t.Fatalf("b usage %+v", ub)
	}
	var n int
	e.st.DB.QueryRow(`SELECT count(*) FROM embeddings WHERE user_id = $1`, id).Scan(&n)
	if n != 0 {
		t.Fatalf("embeddings left: %d", n)
	}
}

// deleteAccount walks the confirm step: preview, wrong phrase, then delete.
func deleteAccount(t *testing.T, e *env, memories int) {
	t.Helper()
	csrf := e.csrf()
	resp, body := e.post("/account/delete", url.Values{"csrf": {csrf}})
	if resp.StatusCode != 200 || !strings.Contains(body, "Delete your account?") || !strings.Contains(body, "30 days") || !strings.Contains(body, deleteAcctPhrase) {
		t.Fatalf("preview: %d %s", resp.StatusCode, body)
	}
	n := strconv.Itoa(memories)
	for _, f := range []url.Values{{"confirm_count": {n}}, {"confirm_count": {n}, "confirm": {"yes"}}, {"confirm": {deleteAcctPhrase}}, {"confirm_count": {"99"}, "confirm": {deleteAcctPhrase}}} {
		f.Set("csrf", csrf)
		if r, _ := e.post("/account/delete", f); r.StatusCode == http.StatusSeeOther {
			t.Fatalf("deleted without full confirmation: %v", f)
		}
		if r, _ := e.get("/memories"); r.StatusCode != 200 {
			t.Fatalf("session lost after refused delete: %v", f)
		}
	}
	resp, _ = e.post("/account/delete", url.Values{"csrf": {csrf}, "confirm_count": {n}, "confirm": {deleteAcctPhrase}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestAccountDeleteCutsAccessImmediately(t *testing.T) {
	e, _, id := memEnv(t)
	tok, _, err := e.st.CreateAgentToken(t.Context(), id, "tunnel")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.st.ResolveBearer(t.Context(), tok); err != nil {
		t.Fatal(err)
	}
	deleteAccount(t, e, 0)
	if _, _, err := e.st.ResolveBearer(t.Context(), tok); err == nil {
		t.Fatal("token still valid")
	}
	if r, _ := e.get("/memories"); r.StatusCode != http.StatusFound || !strings.HasPrefix(r.Header.Get("Location"), "/auth/google/start") {
		t.Fatalf("session still works: %d %q", r.StatusCode, r.Header.Get("Location"))
	}
	var n int
	e.st.DB.QueryRow(`SELECT count(*) FROM oauth_tokens WHERE user_id = $1`, id).Scan(&n)
	if n != 0 {
		t.Fatal("oauth tokens remain")
	}
}

func TestAccountDeleteThenSigninRestores(t *testing.T) {
	e, c, id := memEnv(t)
	putMem(t, c, "still here")
	tok, _, _ := e.st.CreateAgentToken(t.Context(), id, "tunnel")
	deleteAccount(t, e, 1)
	e.newClient()
	e.idp.ids["c"] = alice()
	if resp, _ := e.callback(e.start(""), "c"); resp.StatusCode != 200 {
		t.Fatalf("signin %d", resp.StatusCode)
	}
	_, body := e.get("/memories")
	if !strings.Contains(body, "still here") {
		t.Fatalf("data missing: %s", body)
	}
	_, body = e.get("/connections")
	if strings.Contains(body, "/revoke") {
		t.Fatalf("connection came back: %s", body)
	}
	if _, _, err := e.st.ResolveBearer(t.Context(), tok); err == nil {
		t.Fatal("old token valid again")
	}
}

var regexpBatch = regexp.MustCompile(`/memories/restore/([^"]+)"`)

func TestMemoriesListFirstWithRelativeTimeAndCleanUpCollapsed(t *testing.T) {
	e, c, _ := memEnv(t)
	putMem(t, c, "a quiet fact")
	_, body := e.get("/memories")
	for _, want := range []string{"just now", `title="`, "<summary>Clean up</summary>", `href="/compaction"`, `href="/memories/deleted"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "<details class=\"cleanup section\" open") {
		t.Fatal("Clean up must start collapsed")
	}
}

func TestMemoriesSelectPageFallback(t *testing.T) {
	e, c, _ := memEnv(t)
	putMem(t, c, "one")
	putMem(t, c, "two")
	_, body := e.get("/memories")
	if strings.Contains(body, " checked>") || !strings.Contains(body, "Select all on this page") {
		t.Fatalf("plain page wrong: %s", body)
	}
	_, body = e.get("/memories?page=1&select=page")
	if strings.Count(body, `name="ids"`) != 2 || strings.Count(body, `name="ids" value`) != strings.Count(body, " checked>") || !strings.Contains(body, "2 selected") {
		t.Fatalf("select=page wrong: %s", body)
	}
}
