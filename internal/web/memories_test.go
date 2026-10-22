package web

import (
	"net/http"
	"net/url"
	"regexp"
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
	resp, _ := e.post("/memories/delete", url.Values{"csrf": {e.csrf()}, "older_than": {"30"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status %d", resp.StatusCode)
	}
	_, body := e.get("/memories")
	if strings.Contains(body, "ancient") || !strings.Contains(body, "fresh") {
		t.Fatalf("wrong rows: %s", body)
	}
}

func TestClearBrainNeedsConfirmation(t *testing.T) {
	e, c, _ := memEnv(t)
	putMem(t, c, "one")
	putMem(t, c, "two")
	csrf := e.csrf()
	for _, conf := range []string{"", "delete my brain please"} {
		resp, _ := e.post("/memories/delete", url.Values{"csrf": {csrf}, "all": {"1"}, "confirm": {conf}})
		if resp.StatusCode != http.StatusBadRequest || liveCount(t, c) != 2 {
			t.Fatalf("confirm %q: status %d live %d", conf, resp.StatusCode, liveCount(t, c))
		}
	}
	resp, _ := e.post("/memories/delete", url.Values{"csrf": {csrf}, "all": {"1"}, "confirm": {"delete my brain"}})
	if resp.StatusCode != http.StatusSeeOther || liveCount(t, c) != 0 {
		t.Fatalf("status %d live %d", resp.StatusCode, liveCount(t, c))
	}
}

func TestRestoreBatchFromPage(t *testing.T) {
	e, c, _ := memEnv(t)
	id1 := putMem(t, c, "keep me")
	csrf := e.csrf()
	e.post("/memories/delete", url.Values{"csrf": {csrf}, "ids": {id1}})
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
	e.post("/memories/delete", url.Values{"csrf": {e.csrf()}, "ids": {bm}})
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

func TestAccountDeleteCutsAccessImmediately(t *testing.T) {
	e, _, id := memEnv(t)
	tok, _, err := e.st.CreateAgentToken(t.Context(), id, "tunnel")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.st.ResolveBearer(t.Context(), tok); err != nil {
		t.Fatal(err)
	}
	resp, _ := e.post("/account/delete", url.Values{"csrf": {e.csrf()}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status %d", resp.StatusCode)
	}
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
	e.post("/account/delete", url.Values{"csrf": {e.csrf()}})
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
	if strings.Contains(body, "tunnel") {
		t.Fatalf("connection came back: %s", body)
	}
	if _, _, err := e.st.ResolveBearer(t.Context(), tok); err == nil {
		t.Fatal("old token valid again")
	}
}

var regexpBatch = regexp.MustCompile(`/memories/restore/([^"]+)"`)
